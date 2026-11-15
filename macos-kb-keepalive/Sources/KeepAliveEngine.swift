import Foundation
import CoreBluetooth

/// BLE 防休眠引擎
/// 用户视角的三个模式：
///   .continuous —— 持续防休眠（App 运行期间键盘永不休眠）
///   .timed(d)   —— 限时防休眠 d 秒，到期自动停止，键盘恢复自然休眠
///   .off        —— 不保护，也不保持连接（键盘可正常入睡省电）
/// 轮询间隔是实现细节（固定 60 秒），不暴露给界面
///
/// 线程约定：CBCentralManager 与全部定时器都跑在主队列，本类所有可变状态
/// 只在主线程读写。若未来改用专用 BLE 队列，必须重新审计全部字段。
final class KeepAliveEngine: NSObject, CBCentralManagerDelegate, CBPeripheralDelegate {
    enum KeepAwakeMode: Equatable {
        case continuous
        case timed(TimeInterval)
        case off
    }

    enum LinkState: String {
        case ready = "已连接"
        case searching = "寻找键盘"
        case waiting = "等待重连"
        case connecting = "连接中"
        case standby = "未连接（不保护时自动放手）"
        case bluetoothOff = "蓝牙未开启"
        case unauthorized = "无蓝牙权限"
    }

    private static let hidService = CBUUID(string: "1812")
    private static let batteryService = CBUUID(string: "180F")
    private static let batteryLevel = CBUUID(string: "2A19")
    /// 菜单允许的限时时长档位（秒）
    private static let timedDurations: Set<TimeInterval> = [1800, 3600, 7200, 14400]
    private static let isoFmt = ISO8601DateFormatter()

    var onStateChange: (() -> Void)?
    var onBattery: ((Int) -> Void)?

    private var central: CBCentralManager!
    private var kb: CBPeripheral?
    private var batteryChar: CBCharacteristic?
    private var tick: DispatchSourceTimer?
    private var nextReadAt = Date.distantFuture
    private var scanStartedAt: Date?
    private let pollInterval: TimeInterval = 60

    // 重连调度：可取消、可合并、带指数退避（防连接风暴）
    private var reconnectItem: DispatchWorkItem?
    private var backoff: TimeInterval = 2
    private var scanBackoff: TimeInterval = 2
    private var lastCBState: CBManagerState = .unknown

    // 看门狗：connecting 卡死 / 读请求无响应
    private var connectingSince: Date?
    private var pendingReadAt: Date?
    // 扫描退避到达上限时告警一次（链路恢复后重置）
    private var scanCapWarned = false

    private(set) var mode: KeepAwakeMode = .continuous
    private(set) var timedDeadline: Date?
    private(set) var linkState: LinkState = .searching {
        didSet {
            if linkState != oldValue {
                AppLogger.shared.info("链路状态 → \(linkState.rawValue)")
                onStateChange?()
            }
        }
    }
    private(set) var battery: Int = -1
    private(set) var lastReadAt: Date?
    private(set) var readAttemptCount = 0   // 自动保活读的发出次数
    private(set) var readSuccessCount = 0   // 收到响应的次数（界面"已保活 N 次"展示它）
    private(set) var manualReadCount = 0    // 手动诊断读，不计入保活统计
    private(set) var reconnectCount = 0
    private(set) var lastDisconnectReason: String?
    private var lastSuccessAt: Date?

    /// 持久化的目标设备 UUID：首次连接成功后记住，之后按 UUID 精确连接（避免连到别的键盘）
    private var targetID: UUID? {
        get { UserDefaults.standard.string(forKey: "peripheralUUID").flatMap(UUID.init(uuidString:)) }
        set { UserDefaults.standard.set(newValue?.uuidString, forKey: "peripheralUUID") }
    }

    // MARK: - 供界面展示的派生状态

    var statusText: String {
        switch mode {
        case .off:         return "防休眠已关闭"
        case .continuous:  return "持续防休眠中"
        case .timed:       return "限时防休眠中"
        }
    }

    var isProtecting: Bool {
        if case .off = mode { return false }
        return linkState == .ready
    }

    /// 限时模式剩余秒数；非限时模式返回 nil
    var timedRemaining: TimeInterval? {
        if case .timed = mode, let d = timedDeadline { return max(0, d.timeIntervalSinceNow) }
        return nil
    }

    var secondsUntilNextRead: Int? {
        guard isProtecting, batteryChar != nil else { return nil }
        return max(0, Int(nextReadAt.timeIntervalSinceNow))
    }

    // MARK: - 启动与模式

    func start() {
        dispatchPrecondition(condition: .onQueue(.main))
        restoreMode()
        central = CBCentralManager(delegate: self, queue: .main)
        let t = DispatchSource.makeTimerSource(queue: .main)
        t.schedule(deadline: .now() + 1, repeating: 1)
        t.setEventHandler { [weak self] in self?.tickOnce() }
        t.resume()
        tick = t
        AppLogger.shared.info("启动模式：\(modeLogName)")
    }

    func setMode(_ newMode: KeepAwakeMode) {
        if newMode == mode {
            // 同值重选：限时模式视为"续期"（重新计时）；其余模式无操作
            guard case .timed(let d) = newMode else { return }
            timedDeadline = Date().addingTimeInterval(d)
            persistTimedDeadline()
            AppLogger.shared.info("限时防休眠续期，重新计时")
            onStateChange?()
            return
        }
        mode = newMode
        switch newMode {
        case .continuous:
            UserDefaults.standard.set("continuous", forKey: "keepAwakeMode")
            UserDefaults.standard.removeObject(forKey: "timedDeadline")
        case .timed(let d):
            timedDeadline = Date().addingTimeInterval(d)
            UserDefaults.standard.set("timed", forKey: "keepAwakeMode")
            UserDefaults.standard.set(d, forKey: "timedDuration")
            persistTimedDeadline()
        case .off:
            timedDeadline = nil
            UserDefaults.standard.set("off", forKey: "keepAwakeMode")
            UserDefaults.standard.removeObject(forKey: "timedDeadline")
            releaseLink()   // 真正放手：停扫、断连、清引用，键盘可自然入睡
            linkState = .standby
            AppLogger.shared.info("模式 → \(modeLogName)")
            onStateChange?()
            return
        }
        nextReadAt = Date()  // 立即发一次，让保活节奏是新鲜的
        if linkState != .ready { connect() }
        AppLogger.shared.info("模式 → \(modeLogName)")
        onStateChange?()
    }

    /// 手动触发一次读（诊断用，不计入保活统计）
    func fireNow() {
        guard isProtecting, batteryChar != nil else { return }
        fireRead(manual: true)
    }

    private var modeLogName: String {
        switch mode {
        case .continuous: return "持续防休眠"
        case .timed(let d): return "限时防休眠 \(Int(d/60)) 分钟"
        case .off: return "关闭（键盘自然休眠）"
        }
    }

    private func persistTimedDeadline() {
        if let d = timedDeadline {
            UserDefaults.standard.set(Self.isoFmt.string(from: d), forKey: "timedDeadline")
        }
    }

    private func restoreMode() {
        switch UserDefaults.standard.string(forKey: "keepAwakeMode") {
        case "off":
            mode = .off
            linkState = .standby
        case "timed":
            let d = UserDefaults.standard.double(forKey: "timedDuration")
            guard Self.timedDurations.contains(d) else {
                AppLogger.shared.warn("持久化的限时时长 \(Int(d)) 秒不在允许档位，回退持续防休眠")
                mode = .continuous
                return
            }
            if let iso = UserDefaults.standard.string(forKey: "timedDeadline"),
               let dl = Self.isoFmt.date(from: iso) {
                guard dl > Date() else {
                    // 上次设的限时已过期：按到期语义恢复为关闭
                    mode = .off
                    linkState = .standby
                    return
                }
                mode = .timed(d)
                timedDeadline = dl
            } else {
                mode = .timed(d)
                timedDeadline = Date().addingTimeInterval(d)   // 旧版本只存了时长：重新计时
            }
        default:
            mode = .continuous
        }
    }

    // MARK: - 周期调度（1 秒 tick：看门狗 + 保活节奏）

    private func tickOnce() {
        // 看门狗 1：连接/服务发现卡死（12 秒无进展）→ 重置链路重连
        if linkState == .connecting, let t = connectingSince, Date().timeIntervalSince(t) > 12 {
            AppLogger.shared.warn("连接/服务发现超时（12 秒），重置链路")
            resetLink(reason: "连接超时")
            return
        }
        // 看门狗 2：读请求发出 8 秒无响应 → 判定链路假死（防止"假保活"）。
        // 只在 .ready 时生效——链路已断时残留的 pendingReadAt 由断连回调负责清
        if linkState == .ready, let p = pendingReadAt, Date().timeIntervalSince(p) > 8 {
            AppLogger.shared.warn("保活读超时（8 秒无响应），判定链路假死，重连")
            resetLink(reason: "读超时")
            return
        }
        // 扫描退避：20 秒无果 → 停扫，按退避间隔再试（键盘长期不在时不过度扫描）
        if linkState == .searching, let s = scanStartedAt, Date().timeIntervalSince(s) > 20 {
            central.stopScan()
            scanStartedAt = nil
            scanBackoff = min(scanBackoff * 2, 300)
            linkState = .waiting   // 退避等待期间如实展示，而不是"寻找键盘"
            if scanBackoff >= 300 && !scanCapWarned {
                scanCapWarned = true
                AppLogger.shared.warn("键盘长时间未发现（扫描退避已达上限），请检查键盘电量/开关/配对状态")
            }
            AppLogger.shared.info("扫描 20 秒未发现目标，\(Int(scanBackoff)) 秒后再试")
            scheduleReconnect(after: scanBackoff)
            return
        }
        guard mode != .off else { return }
        // 蓝牙权限恢复后自动拉起（不重建 manager）
        if linkState == .unauthorized, central.state == .poweredOn {
            connect()
            return
        }
        // 限时到期：自动停止保护
        if case .timed = mode, let d = timedDeadline, Date() >= d {
            AppLogger.shared.info("限时防休眠到期，恢复键盘自然休眠")
            setMode(.off)
            return
        }
        // 键盘作为系统 HID 已连接时立刻吸附（每秒轮询，纯内存查询无射频开销，
        // 扫描退避等待期间也能秒级恢复）
        if linkState == .searching || linkState == .waiting, let found = systemConnectedTarget() {
            adopt(found, source: "system")
            return
        }
        guard linkState == .ready, batteryChar != nil, Date() >= nextReadAt else { return }
        fireRead()
    }

    /// 系统已连接外设里明确叫 M150 的目标（无则 nil）
    private func systemConnectedTarget() -> CBPeripheral? {
        let connected = central.retrieveConnectedPeripherals(withServices: [Self.hidService, Self.batteryService])
        return connected.first(where: { ($0.name ?? "").contains("M150") })
    }

    private func fireRead(manual: Bool = false) {
        guard let kb, let ch = batteryChar else { return }
        nextReadAt = Date().addingTimeInterval(pollInterval)
        pendingReadAt = Date()
        if manual {
            manualReadCount += 1
            AppLogger.shared.info("手动读 #\(manualReadCount) → 读请求已发出")
        } else {
            readAttemptCount += 1
            AppLogger.shared.info("保活 #\(readAttemptCount) → 读请求已发出")
        }
        kb.readValue(for: ch)
    }

    // MARK: - 连接管理

    private func connect() {
        guard mode != .off else { linkState = .standby; return }
        guard central.state == .poweredOn else {
            // 蓝牙不可用：反映真实状态（手动切模式时也正确），等状态回调拉起
            switch central.state {
            case .unauthorized: linkState = .unauthorized
            case .poweredOff, .resetting: linkState = .bluetoothOff
            default: break
            }
            return
        }
        if let kb {
            if kb.state == .connecting { return }           // 已在连接中，交给看门狗
            if kb.state == .connected, batteryChar != nil { linkState = .ready; return }
        }
        // 1) 上次成功连过的设备：按 UUID 精确匹配
        if let id = targetID,
           let p = central.retrievePeripherals(withIdentifiers: [id]).first {
            adopt(p, source: "uuid")
            return
        }
        // 2) 系统已连接设备里明确叫 M150 的（键盘被系统当 HID 占着时不广播）
        if let found = systemConnectedTarget() {
            adopt(found, source: "system")
            return
        }
        // 3) 都没有 → 主动扫描。不过滤服务 UUID：实测这把键盘的广播里
        //    不带 1812/180F，按服务过滤会永远发现不了它；改为广播名匹配 + 退避控噪
        linkState = .searching
        scanStartedAt = Date()
        central.scanForPeripherals(withServices: nil,
                                   options: [CBCentralManagerScanOptionAllowDuplicatesKey: false])
    }

    /// 采纳目标外设：统一入口，保证旧引用/旧特征值被清干净。
    /// source 只用于日志标明吸附路径（uuid / system / scan）
    private func adopt(_ p: CBPeripheral, source: String) {
        AppLogger.shared.info("采纳目标 \(p.name ?? "?")（来源：\(source)）")
        central.stopScan()
        scanStartedAt = nil
        cancelReconnect()
        kb = p
        p.delegate = self
        batteryChar = nil
        battery = -1
        pendingReadAt = nil
        targetID = p.identifier
        linkState = .connecting
        connectingSince = Date()
        if p.state == .connected {
            // 系统已经连着它（如 HID）：connect 是 no-op 且不会回调 didConnect，
            // 直接发现服务，否则要白等 12 秒看门狗
            p.discoverServices([Self.batteryService])
            return
        }
        central.connect(p, options: nil)
    }

    /// 断开并清空链路全部引用（不改变 linkState，由调用方决定去向）
    private func releaseLink() {
        cancelReconnect()
        central?.stopScan()
        scanStartedAt = nil
        if let kb { central?.cancelPeripheralConnection(kb) }
        kb = nil
        batteryChar = nil
        battery = -1
        nextReadAt = .distantFuture
        pendingReadAt = nil
        connectingSince = nil
    }

    /// 主动重置链路（看门狗/读失败）：断开、清引用、退避后重连
    private func resetLink(reason: String) {
        cancelReconnect()
        central.stopScan()
        scanStartedAt = nil
        pendingReadAt = nil
        connectingSince = nil
        if let kb { central.cancelPeripheralConnection(kb) }
        kb = nil
        batteryChar = nil
        battery = -1
        lastDisconnectReason = reason
        reconnectCount += 1
        linkState = .searching
        backoff = min(backoff * 2, 60)
        scheduleReconnect()
    }

    // MARK: - 重连调度（可取消、可合并、带退避）

    private func scheduleReconnect(after delay: TimeInterval? = nil) {
        reconnectItem?.cancel()
        let item = DispatchWorkItem { [weak self] in
            guard let self else { return }
            self.reconnectItem = nil
            self.connect()
        }
        reconnectItem = item
        DispatchQueue.main.asyncAfter(deadline: .now() + (delay ?? backoff), execute: item)
    }

    private func cancelReconnect() {
        reconnectItem?.cancel()
        reconnectItem = nil
    }

    /// 链路确认健康：清掉待重连任务、重置退避
    private func noteLinkHealthy() {
        cancelReconnect()
        backoff = 2
        scanBackoff = 2
        scanCapWarned = false
    }

    // MARK: - CBCentralManagerDelegate

    func centralManagerDidUpdateState(_ c: CBCentralManager) {
        defer { lastCBState = c.state }
        switch c.state {
        case .poweredOn:
            if lastCBState != .poweredOn { connect() }   // 去抖：状态抖动不重复拉起
        case .unauthorized:
            linkState = .unauthorized
            AppLogger.shared.warn("App 未获得蓝牙权限，请在系统设置中允许")
        case .poweredOff, .resetting:
            releaseLink()
            linkState = .bluetoothOff
        default:
            break
        }
    }

    func centralManager(_ central: CBCentralManager, didDiscover peripheral: CBPeripheral,
                        advertisementData: [String: Any], rssi RSSI: NSNumber) {
        let name = peripheral.name ?? (advertisementData[CBAdvertisementDataLocalNameKey] as? String) ?? ""
        guard name.contains("M150") else { return }
        AppLogger.shared.info("扫描发现 \(name) (RSSI \(RSSI))，连接")
        adopt(peripheral, source: "scan")
    }

    func centralManager(_ central: CBCentralManager, didConnect peripheral: CBPeripheral) {
        guard peripheral === kb else {
            central.cancelPeripheralConnection(peripheral)   // 非当前目标的陈旧回调
            return
        }
        connectingSince = Date()   // 服务发现阶段重新计时
        peripheral.discoverServices([Self.batteryService])
    }

    func centralManager(_ central: CBCentralManager, didFailToConnect peripheral: CBPeripheral, error: Error?) {
        guard mode != .off else { linkState = .standby; return }
        // resetLink/adopt 已自行调度重连（kb 已被清空），避免二次退避
        guard peripheral.identifier == kb?.identifier else { return }
        // 上一代连接的迟到回调：本代连接已在发起（kb.state == .connecting），
        // 忽略它，避免退避翻倍过快、覆盖刚发起的连接
        if kb?.state == .connecting { return }
        kb = nil
        batteryChar = nil
        pendingReadAt = nil
        connectingSince = nil
        let reason = error.map(String.init(describing:)) ?? "?"
        lastDisconnectReason = "连接失败: \(reason)"
        reconnectCount += 1
        AppLogger.shared.warn("连接失败: \(reason)，\(Int(backoff)) 秒后重试")
        linkState = .searching
        backoff = min(backoff * 2, 60)
        scheduleReconnect()
    }

    func centralManager(_ central: CBCentralManager, didDisconnectPeripheral peripheral: CBPeripheral, error: Error?) {
        batteryChar = nil
        battery = -1
        guard mode != .off else {
            linkState = .standby   // 关闭模式下键盘入睡是预期行为，不拉起重连
            return
        }
        // resetLink/adopt 已自行调度重连（kb 已被清空），避免二次退避
        guard peripheral.identifier == kb?.identifier else { return }
        // 上一代连接的迟到回调：本代连接已在发起（kb.state == .connecting），忽略它，
        // 避免退避翻倍过快、覆盖刚发起的连接。注意：这里必须刷新 connectingSince
        // 让 12 秒看门狗继续兜底——若把清理放在守卫之前，它被置空后链路会永久
        // 卡在 .connecting（connect() 的前置守卫也会挡住一切恢复路径）
        if kb?.state == .connecting {
            pendingReadAt = nil
            connectingSince = Date()
            AppLogger.shared.info("连接发起期间收到上一代断连回调，已忽略（12 秒看门狗兜底）")
            return
        }
        pendingReadAt = nil      // 未完成的读随链路一起作废，避免看门狗误触发
        connectingSince = nil
        kb = nil
        let reason = error.map(String.init(describing:)) ?? "正常"
        lastDisconnectReason = "断开: \(reason)"
        reconnectCount += 1
        AppLogger.shared.warn("键盘断开: \(reason)，\(Int(backoff)) 秒后重连")
        linkState = .searching
        backoff = min(backoff * 2, 60)
        scheduleReconnect()
    }

    // MARK: - CBPeripheralDelegate

    func peripheral(_ p: CBPeripheral, didDiscoverServices error: Error?) {
        guard error == nil,
              let s = p.services?.first(where: { $0.uuid == Self.batteryService }) else {
            AppLogger.shared.warn("发现服务失败: \(error.map(String.init(describing:)) ?? "无 Battery 服务")，重连")
            resetLink(reason: "服务发现失败")
            return
        }
        p.discoverCharacteristics([Self.batteryLevel], for: s)
    }

    func peripheral(_ p: CBPeripheral, didDiscoverCharacteristicsFor service: CBService, error: Error?) {
        guard error == nil,
              let ch = service.characteristics?.first(where: { $0.uuid == Self.batteryLevel }) else {
            AppLogger.shared.warn("发现 Battery Level 特征值失败: \(error.map(String.init(describing:)) ?? "特征值缺失")，重连")
            resetLink(reason: "特征值发现失败")
            return
        }
        connectingSince = nil
        batteryChar = ch
        linkState = .ready
        noteLinkHealthy()
        nextReadAt = Date()   // 立即补一次读，尽快恢复保护
        AppLogger.shared.info("保活通道就绪（内部轮询 \(Int(pollInterval)) 秒）")
    }

    func peripheral(_ p: CBPeripheral, didUpdateValueFor characteristic: CBCharacteristic, error: Error?) {
        pendingReadAt = nil
        guard error == nil, characteristic.uuid == Self.batteryLevel,
              let v = characteristic.value, v.count >= 1 else {
            AppLogger.shared.warn("保活读失败: \(error.map(String.init(describing:)) ?? "空响应")，重连")
            resetLink(reason: "读失败")
            return
        }
        let now = Date()
        // 回归观测：距上次成功读超过 2×轮询间隔 → 给链路假死留下痕迹
        if let prev = lastSuccessAt, now.timeIntervalSince(prev) > 2 * pollInterval {
            AppLogger.shared.warn("距上次成功保活 \(Int(now.timeIntervalSince(prev))) 秒（> 2×\(Int(pollInterval))s），期间链路可能假死")
        }
        lastSuccessAt = now
        lastReadAt = now
        battery = Int(v[0])
        readSuccessCount += 1
        AppLogger.shared.info("保活 #\(readAttemptCount) ✓ 键盘在线 · 电量 \(battery)%")
        onBattery?(battery)
    }
}
