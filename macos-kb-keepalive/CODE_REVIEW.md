# macos-kb-keepalive 代码 Review

- 审查对象：`Sources/*.swift`（8 个文件，912 行）、`build.sh`、`Info.plist`
- 审查时间：2026-11-15
- 环境：macOS 26.6.2 / Swift 6.3.3 / arm64；实跑进程 PID 18593（`~/Applications/KbKeepAlive.app`，Apple Development 签名，Team KA7MGF3U4W）
- 方法：全量阅读 + 实际构建 + 5442 行真实日志分析 + 3 个最小复现实验（RunLoop 模式、`pipefail`、`Codable` 缺字段）+ `footprint`/`vmmap` 实测内存

---

## 0. 结论速览

**总评：设计方向正确，单实例下的线程模型干净、资源有界、TCC 方案有效；但核心链路存在 4 个"静默失效"类缺陷——出问题时 App 不会崩、不会报警，只会安静地不再保活。当前版本不建议无人值守长期运行，修完 P0 后可以。**

| 级别 | 数量 | 含义 |
|---|---|---|
| **P0 必须修** | 4 | 核心保活会静默失效，或用户显式关闭后仍在工作 |
| **P1 应该修** | 10 | 长期运行的可靠性/正确性隐患，有明确触发条件 |
| **P2 建议** | 19 | 健壮性、可维护性、构建细节 |

**建议修复顺序**：P0-1 → P0-3 → P0-2 → P0-4 → P1-1 → P1-2 → P1-10 → 其余。

**内存结论先说**：实测 `phys_footprint` 仅 **17 MB**（峰值 19 MB），RSS 44–49 MB 主要是 AppKit/CoreBluetooth 的共享只读页，**不是泄漏**。详见 §4.7。

**TCC 结论先说**：签名后的 designated requirement 是
`identifier "com.ming.kbkeepalive" and anchor apple generic and certificate leaf[subject.CN] = "Apple Development: 8613572157391 (U385KDAVH7)" and ...`。
**只要始终用同一张 Apple Development 证书，重建/重装不会丢 TCC**；`build.sh` 里"取第一张 Apple Development 证书"的写法是这条链路唯一的单点风险（P2-3）。

---

## 1. P0 必须修

### P0-1 `.off` 模式既不断连也不停扫描，且 `linkState` 仍停留在「已连接」

- **位置**：`Sources/KeepAliveEngine.swift:113-118`（`.off` 分支）、`:8`（注释里对 off 的定义）、`:21`（`standby` 的文案）、全文件无 `cancelPeripheralConnection`
- **问题**：`setMode(.off)` 只清了会话计数和 deadline，**没有** `stopScan()`、**没有** `cancelPeripheralConnection()`，也没有把 `linkState` 置为 `.standby`。如果切换时已经 `.ready`，`linkState` 会一直保持 `.ready`。
- **为什么是问题**：
  - 代码第 8 行自己定义 off = "不保护，**也不保持连接**（键盘可正常入睡省电）"，第 21 行 `standby` 文案是"未连接（不保护时自动放手）"——实现与规格直接矛盾。
  - 用户点"关闭（键盘自然休眠）"后，App 仍作为 GATT client 与键盘保持连接；菜单信息行仍走 `linkState == .ready` 分支（`MenuBarController.swift:130-132`），只是把"下次保活"改写成"已停发"，并不会显示"未连接"——用户很难看出链路其实还在。
  - 若在扫描中切到 off，扫描要等 `tickOnce` 的 20 秒看门狗（`:164-170`）才停，期间仍在扫。
- **修法**（改 `.off` 分支，S 改动）：

```swift
case .off:
    awakeSince = nil
    awakeReadCount = 0
    timedDeadline = nil
    // 新增：真正"放手"
    central?.stopScan()
    scanStartedAt = nil
    if let kb { central?.cancelPeripheralConnection(kb) }
    kb = nil
    batteryChar = nil
    battery = -1
    nextReadAt = .distantFuture
    linkState = .standby            // 触发 didSet → UI 刷新
    UserDefaults.standard.set("off", forKey: "keepAwakeMode")
```

`didDisconnectPeripheral` 里的 `guard mode != .off` 分支已经会把状态置 `.standby`，与这里兼容。

---

### P0-2 连接/服务发现没有看门狗，`linkState` 可能永久卡在 `.connecting`

- **位置**：`Sources/KeepAliveEngine.swift:194-216`（`connect()`）、`:245-248`（`didConnect`）、`:250-254`（`didFailToConnect`）、`:270-276`（`didDiscoverServices`）、`:278-287`（`didDiscoverCharacteristicsFor`）
- **问题**：
  - `connect()` 之后只依赖 `didConnect` 回调推进状态；若回调丢失、`didConnect` 后 `discoverServices` 无响应、或发现服务/特征值失败，代码在 `:272-274`、`:280-282` 直接 `return`，**没有失败分支、没有重试、没有超时**。
  - 结果：`linkState` 永远停在 `.connecting`。`tickOnce` 的 `guard linkState == .ready` 不通过，`connect()` 也不会再被调用 → 保活彻底停摆，直到用户重启 App。
  - 菜单只会显示"连接中"，没有任何异常提示。
  - 另一个入口：若 `kb` 已经 `.connected`（例如从 off 切回 continuous 但连接从未释放，见 P0-1），`connect()` 会再次 `central.connect`，重复 connect 的行为没有防护，可能产生多余回调或直接无回调。
- **为什么是问题**：这正是"长期常驻"最危险的失败模式——不崩、不报错、用户不知道键盘已经没人管了。CoreBluetooth 的 connect 偶发不回调是已知现象。
- **修法**（M 改动）：加一个 `connectingSince` 时间戳，由 1 秒 tick 统一兜底：

```swift
private var connectingSince: Date?

// connect() 进入时：
connectingSince = Date()

// didConnect / didDisconnect / didFailToConnect：
connectingSince = nil   // 交给下一状态负责

// tickOnce() 最前面（在 searching 分支之前）：
if linkState == .connecting, let t = connectingSince,
   Date().timeIntervalSince(t) > 12 {
    AppLogger.shared.warn("连接/发现超时（12s），重置链路")
    if let kb { central.cancelPeripheralConnection(kb) }
    kb = nil; batteryChar = nil; connectingSince = nil
    scheduleReconnect(after: 2)      // 见 P0-4
    return
}

// didDiscoverServices / didDiscoverCharacteristicsFor 的 error / 找不到分支：
if let e = error { AppLogger.shared.warn("发现服务失败: \(e)") }
scheduleReconnect(after: backoff)
if let kb { central.cancelPeripheralConnection(kb) }
return
```

同时给 `connect()` 加前置守卫：

```swift
if let kb {
    if kb.state == .connecting { return }
    if kb.state == .connected, batteryChar != nil { return }
}
```

---

### P0-3 读请求没有超时，也没有成功/失败判定，链路假死时仍显示「已保活 N 次」

- **位置**：`Sources/KeepAliveEngine.swift:182-190`（`fireRead`）、`:289-298`（`didUpdateValueFor`）、`:74-81`（展示用派生属性）
- **问题**：
  - `fireRead()` 在真正读之前就把 `totalReadCount`/`awakeReadCount` 加一，并立刻把 `nextReadAt` 推后 60 秒。
  - `readValue(for:)` 之后**没有任何超时**。如果外设已进入半睡状态，回调既不成功也不报错，App 会每分钟继续"发一次、计数 +1"，但 `didUpdateValueFor` 永远不回来。
  - UI 的 `secondsUntilNextRead` 只看 `nextReadAt`，所以仍然显示"X 秒后"；日志里会出现 `保活 #N → 读请求已发出` 但**没有** `保活 #N ✓`。这正是"假保活"。
  - `didUpdateValueFor` 里 `lastReadAt = Date()` 在判断 `error` 之前执行（`:290`），失败也被当成"读过"。
- **为什么是问题**：核心功能失效时没有任何可观测信号；用户以为在保护，实际键盘照样休眠。这也解释了"为什么需要看日志才能发现"。
- **修法**（M 改动）：

```swift
private var pendingReadAt: Date?
private(set) var readAttemptCount = 0      // 发出次数（原 totalReadCount 语义）
private(set) var readSuccessCount = 0      // 成功次数（UI 用它显示"已保活"）

private func fireRead() {
    guard let ch = batteryChar, let kb else { return }
    nextReadAt = Date().addingTimeInterval(pollInterval)
    pendingReadAt = Date()
    readAttemptCount += 1
    kb.readValue(for: ch)
}

// tickOnce() 内、fireRead 之前：
if let p = pendingReadAt, Date().timeIntervalSince(p) > 8 {
    pendingReadAt = nil
    AppLogger.shared.warn("读超时（8s），判定链路假死，重连")
    forceReconnect()
    return
}

// didUpdateValueFor：
pendingReadAt = nil
guard error == nil, let v = characteristic.value?.first else {
    AppLogger.shared.warn("保活读失败: ...")
    forceReconnect()
    return
}
lastReadAt = Date()
battery = Int(v)
readSuccessCount += 1
```

`forceReconnect()` = `cancelPeripheralConnection` + 清 `batteryChar` + `scheduleReconnect(after: 2)`。

---

### P0-4 重连没有去抖、没有合并、没有退避，存在连接风暴风险（历史日志已实证）

- **位置**：`Sources/KeepAliveEngine.swift:250-254`、`:256-266`（两处固定 `asyncAfter(2s) { connect() }`）、`:218-221`（每次 `.poweredOn` 直接 `connect()`）、`:164-170`（20 秒扫描看门狗：`stopScan` 后立刻重新 `connect()`，等于没有停顿）
- **问题**：
  - 每次失败/断开都 schedule 一个 2 秒后的 `connect()`，**没有保存 work item、无法取消、无法合并**。成功连接后旧的 retry 仍会执行。
  - 旧 retry 落地时如果已经连上，会走 `retrieveConnectedPeripherals` → 再 `central.connect`，产生重复连接。
  - CB 状态抖动时（`.poweredOff` → `.poweredOn`）每次都触发一次 `connect()`，没有去抖。
  - 全链路只有固定 2 秒间隔，没有指数退避；键盘长时间不在时，每 20 秒重扫一次、每次失败写日志。
- **实证**：`~/Library/Logs/KbKeepAlive/app.log` 中 2026-11-15 00:02–11:19 的 11 小时会话（旧构建，文案为"保活中"）出现 **844 次「连接中→就绪」、784 次「蓝牙未开启」、60 次断开**，也就是典型的状态抖动被放大成连接风暴；同一时段日志里每 ~45 秒一个完整"断电→连接→就绪→读"循环。当前构建（12:16 之后）日志未见抖动，但状态机没有任何去抖/退避代码，条件复现时会复发。
- **为什么是问题**：反复 connect 会与 `didDisconnect` 回调交错，可能把刚建好的链路 cancel 掉；日志会被撑爆（每 2 秒两行 → 512KB 约 7 分钟轮转一次）；持续扫描/连接耗电；也增加 CoreBluetooth 内部状态错乱的概率。
- **修法**（M 改动）：

```swift
private var reconnectItem: DispatchWorkItem?
private var backoff: TimeInterval = 2

private func scheduleReconnect(after delay: TimeInterval? = nil) {
    reconnectItem?.cancel()
    let d = delay ?? backoff
    let item = DispatchWorkItem { [weak self] in
        guard let self else { return }
        self.reconnectItem = nil
        self.connect()
    }
    reconnectItem = item
    DispatchQueue.main.asyncAfter(deadline: .now() + d, execute: item)
}

// 连接成功（didConnect / 就绪）：
reconnectItem?.cancel(); reconnectItem = nil; backoff = 2

// 失败/断开：
backoff = min(backoff * 2, 60)
scheduleReconnect()
```

`centralManagerDidUpdateState` 里对 `.poweredOn` 做去抖（记录 `lastCBState`，只有真正从非 poweredOn 变过来才 `connect()`）；`tickOnce` 的扫描看门狗改成"停扫 → 退避 N 秒 → 再扫"，而不是立刻重扫。

---

## 2. P1 应该修

### P1-1 外设匹配靠名字，且用 `"Keyboard"` 兜底，可能连错键盘

- **位置**：`Sources/KeepAliveEngine.swift:199-204`（`retrieveConnectedPeripherals` + `name.contains("M150")` / `contains("Keyboard")`）、`:234-235`（扫描只认 `M150`）
- **问题**：
  - `retrieveConnectedPeripherals(withServices:)` 返回的是**系统级**已连接外设，不只是本 App 连的。若机器上还连着别的 BLE 键盘（Magic Keyboard 等），`list.first(where: name.contains("Keyboard"))` 会把它选中。
  - 结果：App 去读另一把键盘的电量，`linkState` 变 `.ready`，UI 显示"保护中"，但目标 M150 根本没收到保活流量。
  - 反过来，若 `retrieveConnectedPeripherals` 返回的 `peripheral.name` 为 nil（已连接外设名字不保证有），M150 又不在广播状态 → 扫描永远发现不了，链路永远建不起来。
- **为什么是问题**：静默连错目标 = 核心功能完全失效，且比"连不上"更难发现。
- **修法**（S 改动）：首次连接成功后持久化 UUID，后续按 UUID 找；名字只作为最后的弱匹配，删掉 `"Keyboard"` 兜底：

```swift
private var targetID: UUID? {
    get { UserDefaults.standard.string(forKey: "peripheralUUID").flatMap(UUID.init(uuidString:)) }
    set { UserDefaults.standard.set(newValue?.uuidString, forKey: "peripheralUUID") }
}

// connect():
if let id = targetID, let p = central.retrievePeripherals(withIdentifiers: [id]).first {
    adopt(p); return
}
// 仅当设备名明确含 "M150" 时才采纳；不要在多个候选里猜
if let found = list.first(where: { ($0.name ?? "").contains("M150") }) { adopt(found); return }
// 否则老老实实扫描
```

`adopt(p)` 里统一做 `stopScan`、`kb = p`、`p.delegate = self`、清 `batteryChar = nil`、`linkState = .connecting`、`connectingSince = Date()`、`central.connect(p)`；在 `didConnect` 成功后 `targetID = p.identifier`。

---

### P1-2 CGEventTap 的 runloop source 只挂在 `.defaultMode`，菜单打开期间完全不工作

- **位置**：`Sources/KeyboardMonitor.swift:45-46`
- **问题**：`CFRunLoopAddSource(CFRunLoopGetMain(), src, .defaultMode)`。AppKit 的菜单跟踪运行在 `NSEventTrackingRunLoopMode`，默认模式下的 source 在该模式下**不派发**。
- **实测证据**（本机最小程序，加载 AppKit 后）：
  ```
  AppKit loaded, eventTracking: srcDefault=0  srcCommon=135848  timerCommon=6
  ```
  同一份 source 挂 `.commonModes` 时正常触发 13.5 万次，挂 `.defaultMode` 时 0 次。
- **影响**：
  - 菜单打开期间所有 keyDown 不计数（统计缺口）；菜单里显示的"今日输入"也停在打开前的值。
  - 事件 tap 的 mach port 在 source 不派发期间积压，系统可能以 `tapDisabledByTimeout` 禁用它；虽然 `:66-67` 会 re-enable，但会丢掉这段时间的按键，且用户可能感知到短暂的输入延迟（`.defaultTap` 是 active tap，在输入路径上）。
- **修法**（S 改动）：改成 `.commonModes`（已验证在 tracking 模式下可用）：

```swift
CFRunLoopAddSource(CFRunLoopGetMain(), src, .commonModes)
```

> 注：主队列 `DispatchSourceTimer` 不受此问题影响（实测 tracking 模式下 0.2s 内触发 4 次），所以 `MenuBarController` 的每秒刷新是正常的，不用改。

---

### P1-3 扫描策略过重：全量扫描 + 20 秒\"停一下\"其实是立刻重扫 + 无退避

- **位置**：`Sources/KeepAliveEngine.swift:214`（`scanForPeripherals(withServices: nil)`）、`:164-170`
- **问题**：`withServices: nil` 会扫描并上报环境中所有 BLE 设备；`:164-170` 的注释写"停一下再试"，实际是 `stopScan()` 后马上 `connect()`，而 `connect()` 立刻又 `scanForPeripherals`，等于连续扫描没有间隔。键盘长时间不在（出差/关机）时，App 会永久以 20 秒周期扫全量广播。
- **修法**（S 改动）：扫描过滤 HID/电池服务，并真正退避：

```swift
central.scanForPeripherals(withServices: [
    CBUUID(string: "1812"),   // HID
    CBUUID(string: "180F"),   // Battery
], options: [CBCentralManagerScanOptionAllowDuplicatesKey: false])
```

看门狗改为"停扫 → 指数退避（60s 起，cap 5 分钟）→ 再扫"。

---

### P1-4 蓝牙关闭/断开时不清 `kb`/`batteryChar`，可能持有过期特征值

- **位置**：`Sources/KeepAliveEngine.swift:225-226`（`.poweredOff` 只改状态）、`:194-210`（`connect()` 直接覆盖 `kb`，不清 `batteryChar`）
- **问题**：`batteryChar` 属于某个 `CBPeripheral` 实例。`connect()` 在 `retrieveConnectedPeripherals` 里可能拿到另一个实例并重新赋值 `kb`，但 `batteryChar` 仍是旧实例的特征值。之后 `readValue(for: batteryChar)` 引用的是别的外设的特征值。当前路径下触发条件窄（通常先有 didDisconnect 清空），但一旦 P0-4 的重复 connect 出现就会踩到。
- **修法**（S 改动）：`connect()`/`adopt()` 入口统一 `batteryChar = nil; battery = -1`；`.poweredOff`/`.resetting` 时也清 `kb`、`batteryChar`、`connectingSince` 并 `stopScan()`。

---

### P1-5 限时模式的三个边界问题

- **位置**：`Sources/KeepAliveEngine.swift:101-103`、`:146-159`；`Sources/MenuBarController.swift:89-100`
- **问题**：
  1. **同值重选无法重启倒计时**：`guard newMode != mode else { return }`。已经处于"限时 30 分钟"且只剩 2 分钟时，再点一次"限时 30 分钟"没有任何反应，用户无法续期。
  2. **deadline 不持久化**：`restoreMode` 在 `:155` 用 `Date().addingTimeInterval(dur)` 重新计时。设了 4 小时、3 小时后重启 App，又变回 4 小时。
  3. **菜单栏倒计时不刷新**：`refreshTitle()` 只在 `linkState` 变化或 `onBattery` 时被调用；`onBattery` 依赖读成功（60 秒一次），读失败时标题里的分钟数可能长时间不动。
- **修法**：
  - 同值重选时允许重启：`if newMode == mode, case .timed = newMode { /* 重置 deadline */ }`，或给 timed 菜单项单独 action 强制重启。
  - 持久化绝对 deadline（ISO8601 字符串），`restoreMode` 直接读；已过期则直接 `.off`。
  - `MenuBarController` 在菜单关闭时也保持一个低频（如 10 秒）标题刷新，或在 `menuDidClose` 后启动一个 10 秒 tick 只更新 title（避免每秒刷新常驻）。

---

### P1-6 统计持久化的三个健壮性缺口（会静默丢历史）

- **位置**：`Sources/StatsStore.swift:21-26`（解码失败直接 `Payload()`）、`:55-64`（`flush`）
- **问题**：
  1. 解码失败（文件损坏、字段缺失、旧版本 schema）→ 静默清零；下一次 flush 会用空数据**覆盖**原文件，历史永久丢失。
  2. 实测确认：`Payload` 的默认值不参与合成的 `Decodable`，`{"total":3}`、`{"daily":{...}}`、`{}` 全部 `keyNotFound` 失败（`struct Payload: Codable { var total = 0 }` 并不能容忍缺字段）。
  3. `flush` 在 `:58` 先 `dirty = false` 再写盘；写失败（磁盘满/目录被删）时 `try?` 吞掉错误，脏数据被标记为已落盘，永久丢失。
- **修法**（M 改动）：自定义 `init(from:)` 用 `decodeIfPresent`；解码失败时把坏文件 rename 成 `stats.json.corrupt-<timestamp>` 再重置，并 `warn`；`flush` 写成功后才清 dirty：

```swift
private struct Payload: Codable {
    var total = 0
    var daily: [String: Int] = [:]
    init() {}
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        total = (try? c.decode(Int.self, forKey: .total)) ?? 0
        daily = (try? c.decode([String: Int].self, forKey: .daily)) ?? [:]
    }
}

func flush(force: Bool = false) {
    lock.lock()
    guard dirty || force else { lock.unlock(); return }
    dirty = false
    let snapshot = payload
    lock.unlock()
    do {
        try JSONEncoder().encode(snapshot).write(to: fileURL, options: .atomic)
    } catch {
        lock.lock(); dirty = true; lock.unlock()   // 下次重试
        AppLogger.shared.warn("统计落盘失败: \(error)")
    }
}
```

---

### P1-7 登录自启的状态判断与实际结果不一致

- **位置**：`Sources/LoginItem.swift:10-13`（`isEnabled`）、`:15-30`（`setEnabled`）、`:52-55`（bootstrap 结果被忽略）、`:67-74`（主线程等 `launchctl`）
- **问题**：
  1. `installAgent()` 里 `bootstrap` 失败也 `return true`，只在日志里写"下次登录生效"；UI 勾选状态却依赖"plist 文件存在"（`:12`），于是**显示已开启，实际不会自启**。
  2. 没有处理 `SMAppService.mainApp.status == .requiresApproval`（需要在系统设置里批准）；`register()` 成功但状态是 requiresApproval 时仍记"开"、返回 true，而 `isEnabled` 返回 false，用户看到勾了又弹回。
  3. `run()` 用 `Process.waitUntilExit()` 在主线程跑 `launchctl`（两次），会阻塞主线程数十到数百毫秒；与 P1-2 叠加时更容易触发 tap timeout。
  4. app 路径直接插进 XML（`:40`），家目录含 `&`/`<` 会生成非法 plist。
- **修法**：`setEnabled` 返回并记录最终 `SMAppService.mainApp.status`；`.requiresApproval` 时打开 `x-apple.systempreferences:com.apple.LoginItems-Settings.extension` 并提示；bootstrap 返回码非 0 时返回 false 且不清除日志；`run()` 放 `DispatchQueue.global()` 异步执行；plist 用 `CFXMLCreateStringByEscapingEntities` 转义路径。

---

### P1-8 辅助功能授权重试是\"永久轮询\"，且 tap 失效后无法自愈

- **位置**：`Sources/AppDelegate.swift:23-28`、`:48-58`；`Sources/KeyboardMonitor.swift:15`、`:33`、`:57-59`、`:66-67`
- **问题**：
  1. 用户明确拒绝辅助功能后，`scheduleAuthPoll` 仍每 5 秒 `tapCreate` 一次，永不停止（不刷日志，但一直重试）。
  2. `isCounting` 只看 `tap != nil`。如果运行中用户撤销授权，tap 被 invalidate，`tap` 仍非 nil，`isCounting` 永远为 true，`start()` 永远早退（`:33`），**不会重建**。
  3. 只处理 `tapDisabledByTimeout`，不处理 `tapDisabledByUserInput`；`reenable()` 也不检查 `CFMachPortIsValid`。
- **修法**：`isCounting` 改为 `tap.map(CFMachPortIsValid) ?? false`；回调里两种 disabled 都 re-enable；定期（或在菜单打开时）检测失效并 `tap = nil` 后重启轮询；授权轮询加最大次数或在用户手动关闭后停止。

---

### P1-9 蓝牙未授权/不支持时没有恢复路径

- **位置**：`Sources/KeepAliveEngine.swift:218-230`
- **问题**：`.unauthorized` 只改状态和写一条日志；用户去系统设置打开蓝牙权限后（macOS 通常需要重启 App）没有提示，也没有菜单入口。`.unknown`/`.unsupported` 落入 `default: break`，界面停留在初始的"寻找键盘"。
- **修法**：菜单里为 `.unauthorized` 增加"打开蓝牙设置"项（`x-apple.systempreferences:com.apple.preference.security?Privacy_Bluetooth`）；`.unsupported` 显示明确文案；在 `tickOnce` 里对 `.unauthorized` 每隔 30 秒重读 `central.state`（不重建 manager），状态恢复时 `connect()`。

---

### P1-10 `build.sh` 在找不到证书时会直接失败，ad-hoc 回退分支是死代码

- **位置**：`build.sh:22-29`
- **问题**：`set -euo pipefail` 下，`IDENTITY=$(security find-identity ... | grep -m1 '"Apple Development' | sed ...)` 中 `grep` 无匹配返回 1，`pipefail` 让整个赋值返回 1，脚本**在到达 `if [[ -n ... ]]` 之前就退出**。
- **实测**：
  ```
  zsh -c 'set -euo pipefail; X=$(printf "  1) ..." | grep -m1 "Apple DevelopmentXYZ" | sed ...); echo "REACHED"'
  → exit=1（没有输出 REACHED）
  ```
  也就是说没有 Apple Development 证书的机器上，`./build.sh` 会静默失败，既没有提示，也不会走 `codesign --sign -` 回退。
- **修法**（S 改动）：

```zsh
IDENTITY=$(security find-identity -v -p codesigning 2>/dev/null \
  | grep -m1 '"Apple Development' | sed -E 's/.*"(.*)"$/\1/' || true)
```

并显式 `if [[ -n "$IDENTITY" ]]`，失败时打印醒目警告再走 ad-hoc。

---

## 3. P2 建议（按主题分组）

| # | 位置 | 问题 | 建议 |
|---|---|---|---|
| P2-1 | `StatsStore.swift:35-42` | 每次 keyDown 都 `DateFormatter.string(from:)` + `NSLock`，在输入回调路径上；注释声称"O(1) 不分配大对象"不准确 | 缓存当天 key 和"下个零点"时间戳，只有跨天才重新格式化；锁在纯主线程场景可换成 `os_unfair_lock` 或直接去掉 |
| P2-2 | `AppDelegate.swift:60-65` | `housekeeping` 每 30 秒在主线程 `JSONEncoder` + `Data.write(.atomic)`（仅 dirty 时） | 目前字典很小可接受；若统计字段变多，把编码/写盘挪到背景队列，主线程只取快照 |
| P2-3 | `build.sh:12-14` | 编译出的 `minos 26.0`，但 `Info.plist:23-24` 写 `LSMinimumSystemVersion 13.0`；且只有 arm64 | 加 `-target arm64-apple-macos13.0`（必要时再加 x86_64 后 `lipo`），让 plist 声明与二进制一致 |
| P2-4 | `build.sh:22` | 取"第一张"Apple Development 证书，多证书/证书续期会改变 DR 的 `leaf[subject.CN]`，**TCC 会丢** | 把证书 CN 或 SHA-1 固定成变量；签名后打印 `codesign -d -r-` 的 DR，便于核对 |
| P2-5 | `build.sh:24` | 只有 `--force`，没有 `--options runtime` / `--timestamp` / 签名后 `codesign --verify` | 本机自用可不加；若要分发再加，并在末尾 `codesign --verify --strict` |
| P2-6 | `Info.plist` | 缺 `LSMultipleInstancesProhibited`；`NSBluetoothPeripheralUsageDescription` 已过时；`CFBundleVersion` 固定 1 | 加 `LSMultipleInstancesProhibited=true`（避免登录项+手动启动双实例），删过时键，版本号随构建递增 |
| P2-7 | `KeyboardMonitor.swift:36,38` | `.cgSessionEventTap` + `.headInsertEventTap` + `.defaultTap` 在输入路径上；改 `.listenOnly` 可能触发"输入监视"授权（与现有辅助功能授权不同） | 保留 `.defaultTap`，但把回调降到最低开销；如要改 listenOnly，先确认 TCC 需求再动 |
| P2-8 | `AppLogger.swift:38` | `guard let h = handle else { return }`：句柄失效后所有日志静默丢弃，且没有 `os_log` 兜底 | 加 `os_log` fallback；`rotate()` 失败时保留旧 handle 或定时重试 |
| P2-9 | `AppLogger.swift:40` | `FileHandle.write(_:)` 在写失败（如磁盘满）时可能抛 ObjC 异常 → 崩溃 | 改用 `try handle.write(contentsOf: Data)` 并 catch |
| P2-10 | `AppLogger.swift:46-53` | 轮转只保留 1 份，若 `moveItem` 失败会丢失当前日志；无 fsync | 失败时不要 `closeFile` 到不可恢复；关键路径可省略 fsync |
| P2-11 | `AppDelegate.swift:42-45` | 退出日志走异步队列，`applicationWillTerminate` 返回后进程可能已退出，最后一条日志可能丢 | 加 `AppLogger.flushAndWait()`（`queue.sync {}`）再返回 |
| P2-12 | `KeepAliveEngine.swift:290` | `lastReadAt` 在判断 error 前赋值，失败也刷新 | 移到成功分支（与 P0-3 一起改） |
| P2-13 | `KeepAliveEngine.swift:153` | `d >= 600 ? d : 3600`：持久化值异常时静默改成 1 小时 | 越界时回退到默认值并记日志，或直接 clamp 到菜单允许的档位 |
| P2-14 | `KeepAliveEngine.swift:74-76` | `secondsSinceLastRead`、`awakeSince`、`awakeReadCount`、`currentTimedDuration` 在 UI/日志里都没用到 | 删除或接到菜单上；`awakeReadCount` 目前只增不用 |
| P2-15 | `KeepAliveEngine.swift:128-131` | `fireNow()` 的手动读也计入 `totalReadCount`，与"保活次数"语义混在一起 | 分开 `manualReadCount`，或 UI 标注"含手动" |
| P2-16 | `KeepAliveEngine.swift:291-297` | `didUpdateValueFor` 不校验 characteristic UUID / value 长度（只取 first） | 校验 `characteristic.uuid == CBUUID(string:"2A19")` 且 `value.count >= 1` |
| P2-17 | 全局 | 没有任何针对纯逻辑的测试；`timedRemaining`、`dayFmt`、轮转边界都只能靠手测 | 抽出 `StatsStore`/倒计时/`Payload` 的可测函数，加一个最小的 `swift test` 或脚本化用例 |
| P2-18 | `AppLogger.swift:64-73` | 自检用 RSS 衡量内存，RSS 含共享只读页，容易得出"45MB 很大"的错误结论 | 改用 `task_vm_info.phys_footprint`；实测本 App footprint 17MB（见 §4.7） |
| P2-19 | `KeepAliveEngine.swift:121`、`:194-215` | `setMode` 在蓝牙已关闭（`.poweredOff/.unauthorized`）时仍会调 `connect()`，进而在不可用状态下 `scanForPeripherals`，CoreBluetooth 只打警告，但会反复做无效动作 | `connect()` 开头加 `guard central.state == .poweredOn else { linkState = ...; return }`，状态恢复时由 `centralManagerDidUpdateState` 统一拉起 |

补充说明：

- **App Nap（P2，无实证但值得加保险）**：菜单栏 App 无窗口，理论上可能被 App Nap 合并/延后定时器。现有日志显示整夜 45–60 秒一次读都准时，暂未观察到问题；若要彻底排除，可在 `AppDelegate` 持有 `ProcessInfo.processInfo.beginActivity(options: [.userInitiated], reason: "keep-alive polling")`，退出时 `endActivity`。
- **单实例（P2-6 相关）**：`open -a` 会复用已运行实例，但直接执行 `build/.../KbKeepAlive` 或登录项由 launchd 拉起时可能产生第二个实例，导致按键双计数、stats.json 互相覆盖、两份日志句柄争抢同一文件。加 `LSMultipleInstancesProhibited` 是最低成本防护。
- **壁纸/多显示器**：代码不涉及 `NSScreen`、坐标换算或窗口定位，**检查过、无相关问题**。

---

## 4. 分维度检查结果

### 4.1 并发与线程安全 —— ✅ 未发现数据竞争，但有一个"主线程延迟"隐患

线程地图（已逐一核对）：

| 状态 | 写入方 | 读取方 | 结论 |
|---|---|---|---|
| `KeepAliveEngine` 所有字段（mode/linkState/battery/counters/nextReadAt/kb/batteryChar） | CBCentralManager 回调（`queue: .main`，见 `:92`）、1 秒 tick（`.main`，`:93`）、菜单 action（主线程） | 菜单构建/每秒刷新（主线程） | 全部主线程，无竞争 |
| `KeyboardMonitor.tap` | `start()`/`reenable()`（主线程：tap source 加在主 runloop） | `isCounting`（主线程） | 无竞争 |
| `StatsStore.payload` | CGEventTap 回调（主 runloop → 主线程） | 主线程 | `NSLock` 保护正确；实际单线程，锁是冗余但无害 |
| `AppLogger.handle/pendingBytes` | 专用串行队列（`:11`） | 同队列 | 无竞争；`dirURL` 只读 |
| `LoginItem` 静态状态 | 主线程菜单 action | 主线程 | 无竞争 |

- **无死锁**：`StatsStore` 没有嵌套锁；`AppLogger` 串行队列内不回调外部；tap 回调不取 AppLogger 队列的同步等待。
- **无锁顺序问题**：`flush` 在锁内只做快照，磁盘 I/O 在锁外。
- **一个需要写进注释的隐含约定**：整个 engine 假设 `CBCentralManager(queue: .main)` 且所有 timer 在 main。当前成立，但代码里没有断言/注释保护；未来若有人改成专用 BLE 队列，所有 `private(set)` 字段都会变成数据竞争。建议在 `start()` 处加注释或 `dispatchPrecondition(condition: .onQueue(.main))`。
- **主线程延迟点**：`LoginItem.run()` 的 `waitUntilExit()`（P1-7）和每 30 秒的统计落盘（P2-2）都会占用主线程；与 `.defaultTap` 叠加时可能触发 tap timeout。建议前者异步、后者保持小体积。

### 4.2 CoreBluetooth 生命周期 —— ⚠️ 主路径可用，异常路径有 4 个洞

**正常路径（已核对，没问题）**：

- `.poweredOn` → `connect()`；`.poweredOff/.resetting` → `.bluetoothOff`；`.unauthorized` → `.unauthorized`
- 断连 → 清 `batteryChar`/`battery` → 2 秒后重连（`:256-266`）
- 服务发现只请求 `180F`，特征值只请求 `2A19`，读 Battery Level 的路径正确
- 重连后 `nextReadAt = Date()`（`:285`），会立即补一次读，符合"尽快恢复保护"的预期
- 系统睡眠唤醒后 1 秒 tick 会看到 `nextReadAt` 已过期并立即补读（`DispatchSourceTimer` 睡眠期间不累计，这个行为是对的）

**问题**：

- 连接/发现无超时、无错误重试（P0-2）
- 读无超时（P0-3）
- 重连无去抖/退避/取消（P0-4）
- `.off` 不释放链路（P0-1）
- 辅助问题：`retrieveConnectedPeripherals` 匹配策略（P1-1）、`.poweredOff` 不清 `kb`（P1-4）、`.unauthorized` 无恢复（P1-9）

**真实观测**（`app.log`）：连续保护模式下，链路大约每 10 分钟被系统/键盘断开一次（08:59、09:09、09:19…精确到 10 分 3 秒），App 都能在 2 秒内重连并继续读；11 小时会话累计 845 次成功读、60 次断开。**这说明重连主路径是可靠的，但也说明这条链路本身不稳定，不能假设"连上就一直连着"——更凸显 P0-2/P0-3 看门狗的必要性。**

### 4.3 正确性 —— ✅ 日期/时区/off-by-one 基本正确；⚠️ 模式边界有问题

- **"今日"切分**：`StatsStore` 用 `DateFormatter("yyyy-MM-dd")` 的本地时区日期串做 key，每次 increment 现取现算，跨午夜自动切分；DST 由日历处理。**检查过、没问题**。边界情况：运行中修改系统时区时，静态 `DateFormatter` 可能沿用创建时的默认时区，"今日"会按旧时区切分到重启为止——影响仅限当天计数，可接受。
  - 顺带验证：`yyyy-MM-dd HH:mm:ss` 的 `HH` 是 24 小时制（输出 `06:13:20` 而非 `06:13:20 PM`），不存在 12 小时制解析错误。
- **倒计时**：`timedRemaining = max(0, deadline - now)`，`fmtCountdown` 用 `Int` 截断，显示上只会少不会多，无负数；`secondsUntilNextRead` 也 clamp 到 0。**无 off-by-one**。
- **菜单 tag 越界**：`modeItems` 的 tag 在 `:107` 和 `:171` 都做了范围检查。**安全**。
- **模式切换**：同值重选无法重启（P1-5.1）、deadline 重启后重置（P1-5.2）、标题不随时间刷新（P1-5.3）。
- **`isProtecting`**：只认 `.ready`，与"正在发保活"一致；`.connecting` 时 `fireNow` 是 no-op，合理。
- **`battery = -1` 哨兵**：`refreshTitle` 在 `>= 0` 时才显示百分比，否则"…"，不会显示 -1%。**没问题**。
- **统计计数**：`Int`（64 位）不会溢出；`formatted()` 的本地化显示正常。

### 4.4 资源 —— ✅ 定时器/event tap/日志/统计都有界

- **定时器**：engine 1s tick（`:93-97`）、housekeeping 30s（`AppDelegate:33-37`）、authPoll 5s（成功后自取消，`:51-55`）、菜单 openTimer 1s（`menuDidClose` 取消，`:116-119`）。全部 `[weak self]` 或 self 为常驻单例，**无循环引用、无重复创建**（openTimer 有 `guard nil`）。
- **event tap**：`start()` 有 `tap == nil` 守卫（`:33`），正常情况下只创建一次；未在退出时 invalidate，但进程退出即回收，**无泄漏**。
  - 唯一缺口：tap 失效后 `tap` 不清空（P1-8），属于状态错误而非内存泄漏。
- **日志**：单串行队列写，`pendingBytes` 超过 512KB 轮转，保留 `.1`，磁盘上限约 1MB。**单实例下无竞争、有界**。
- **统计**：`.atomic` 写入（临时文件 + rename），单实例下不会读到半截 JSON；`daily` 每天 1 个 key，一年 365 个，可忽略；无 pruning 但量级不是问题。
- **文件描述符**：实测运行中 `lsof` 共 35 个，其中日志 1 个、`/dev/null` 3 个，其余是系统库映射，**无 fd 泄漏迹象**。
- **内存**：见 §4.7，**无随时间增长证据**。

### 4.5 健壮性 —— ⚠️ 权限/蓝牙关/JSON 损坏有处理，但部分只是"记录状态"

- 缺蓝牙权限：状态显示"无蓝牙权限"，但无恢复入口（P1-9）
- 蓝牙关：状态显示"蓝牙未开启"，开回来自动重连（`:218-221`）——**路径存在**，但无去抖（P0-4）
- 键盘重启/休眠：断连 → 2 秒重连 → 扫描循环——**路径存在**，但无看门狗（P0-2）
- JSON 损坏：能兜住不崩，但会静默清零（P1-6）
- 辅助功能缺失：会提示 + 每 5 秒重试（P1-8）
- 多显示器：不涉及，**N/A**
- 磁盘满：日志写入可能抛异常（P2-9）；统计写失败被吞（P1-6）

### 4.6 构建 / 签名 / Info.plist —— ⚠️ 主路径正确，有一个会导致构建失败的 bug

已验证正确的部分：

- `Info.plist` 在签名**之前**复制 → 被 sealed resources 正确覆盖；`codesign -v --verbose=2` 通过（`valid on disk` + `satisfies its Designated Requirement`）
- `CFBundleExecutable`、`CFBundleIdentifier`、`CFBundleIconFile` 与产物一致
- `LSUIElement=true`，`NSBluetoothAlwaysUsageDescription` 存在
- 签名产物 DR（designated requirement）与已安装副本一致，TeamIdentifier 正确
- `build/KbKeepAlive.app` 与 `~/Applications/KbKeepAlive.app` 的 CDHash 相同，说明当前跑的就是这份源码的构建

问题：

- 无证书时 `pipefail` 直接终止，回退分支不可达（P1-10）
- `minos 26.0` vs `LSMinimumSystemVersion 13.0` 不一致；仅 arm64（P2-3）
- 证书选择取第一张，多证书/续期会改变 TCC 依赖的 DR（P2-4）
- `rm -rf build` 在编译失败时会连带删掉上一份可用构建（可接受，但可改成先编到临时目录再替换）
- 未做 `plutil -lint Info.plist`、`codesign --verify` 的构建后检查
- `--options runtime`/`--timestamp` 缺失（本机自用不影响）

### 4.7 内存专项 —— ✅ 不随时间增长，RSS 数字被高估

- 实测正在运行的 App：`footprint` → **phys_footprint 17 MB，峰值 19 MB**；同期 `ps`/自检 RSS 为 44–49 MB。
- 差异来源：RSS 计入 AppKit/CoreBluetooth/字体等只读共享页；`vmmap` 显示只读库 resident 242MB 但那是全系统共享的，不属于本进程私有。
- 日志证据（更有说服力）：11 小时长会话里，每小时自检 RSS 是**单调下降**的——01:03 49.1MB → 02:03 49.0MB → 03:03 46.4MB → 05:04 40.4MB → 07:05 38.2MB → 09:05 37.3MB → 11:06 37.2MB，期间保活 35→829 次。这是系统回收共享页，不是内存增长。
- 新启动后的 70–77MB（11:20–11:41 那几个会话）同样是框架 page-in / 页缓存未回收，跑一会儿就回落（12:22:41 为 38.4MB）。
- 代码层面没有 per-event 分配累积：tap 回调只有计数；日志行是临时值；`daily` 每天 1 项；重连闭包最多 1–2 个。
- **结论：内存不是问题，不需要为 37–49MB 这个数字做优化；建议把自检指标换成 `phys_footprint`，避免以后误判。**

### 4.8 TCC 专项 —— ✅ 方案有效，前提是"同一张证书"

- 已读出现有签名的 DR：
  ```
  identifier "com.ming.kbkeepalive"
  and anchor apple generic
  and certificate leaf[subject.CN] = "Apple Development: 8613572157391 (U385KDAVH7)"
  and certificate 1[field.1.2.840.113635.100.6.2.1] exists
  ```
  TCC 的 accessibility / bluetooth 记录绑定的是这个 DR，而不是 cdhash。**只要证书不变，`rm -rf build` + 重新编译 + 覆盖安装都不会丢授权**，README 的说法成立。
- 破坏条件（需要避免）：
  1. 回退 ad-hoc 签名（DR 变成 cdhash，必丢）
  2. 换了另一张 Apple Development 证书（CN 变了）
  3. 证书续期/重新签发导致 CN 变化
  4. 多张 Apple Development 证书时 `grep -m1` 选到不同的那张（P2-4）
- 建议：把证书 CN 固定进 `build.sh`，签名后打印/校验 DR；如果发现 DR 变了，明确提示"需要重新授权"，而不是让用户以为 TCC 坏了。

---

## 5. 建议的修复顺序（可直接当任务清单）

**第一批（半天内，解决"静默失效"）**

- [ ] P0-1 `setMode(.off)` 里停扫 + `cancelPeripheralConnection` + 清 `kb/batteryChar` + `linkState = .standby`
- [ ] P0-3 `pendingReadAt` 读超时（8s）+ `readAttemptCount`/`readSuccessCount` 分离 + 失败触发重连
- [ ] P0-2 `connectingSince` + 12s 连接看门狗 + `didDiscover*` 错误分支重连
- [ ] P0-4 `scheduleReconnect`（可取消 work item）+ 指数退避 + `connect()` 状态守卫 + CB 状态去抖

**第二批（一两天，解决长期可靠性）**

- [ ] P1-1 持久化 peripheral UUID，按 UUID 连接，删除 `"Keyboard"` 兜底
- [ ] P1-2 `CFRunLoopAddSource(..., .commonModes)`（一行，已验证）
- [ ] P1-10 `build.sh` 的 `|| true` + 空值判断，修好 ad-hoc 回退
- [ ] P1-3 扫描加 `withServices` 过滤 + 真正退避
- [ ] P1-4 连接入口统一清 `batteryChar`
- [ ] P1-6 `Payload` 容错解码 + 坏文件备份 + flush 成功后才清 dirty
- [ ] P1-7 登录项状态以 `SMAppService.status` 为准 + `.requiresApproval` 处理 + 异步 `launchctl`
- [ ] P1-8 tap 失效检测 + 两种 disabled 事件 + 轮询上限
- [ ] P1-9 蓝牙未授权/不支持的菜单入口与恢复
- [ ] P1-5 限时模式三处边界

**第三批（有空再做）**

- [ ] P2-1 统计日期 key 缓存；P2-3 构建 target；P2-4 固定证书；P2-6 单实例；P2-8/9 日志错误处理；P2-11 退出前 flush；P2-18 footprint 指标；P2-19 poweredOff 守卫；其余 P2

**建议加两条回归观测**（不需要测试框架）：

1. 每次读到 `didUpdateValueFor` 时校验"距上次成功读 > 2×pollInterval"并写 WARN——给假死留痕。
2. 启动/每小时自检里输出 `phys_footprint`、`readSuccessCount`、`reconnectCount`、`lastDisconnectReason`——长期常驻出问题时能一眼定位。

---

## 6. 总体结论：这个 App 长期常驻是否可靠？

**当前版本：能跑，但不算可靠，不建议无人值守长期挂机。**

- **好的部分**：结构简单清晰；所有可变状态收敛在主线程，没有数据竞争；定时器/日志/统计都有界；内存实测 17MB 且不随时间增长；TCC 用固定证书的方案是对的；断连重连的主路径在真实环境中验证过（845 次读 / 11 小时）。
- **致命的部分**：失败模式是**静默**的。连接卡在 `.connecting`、读回调丢失、外设匹配到别的键盘——这三种情况都不会崩溃、不会弹窗、菜单还会显示"保护中"，用户没有任何办法知道键盘其实已经睡了。对"防休眠"这种后台工具来说，静默失效比崩溃更糟。
- **结论**：修完 4 个 P0 + P1-1/P1-2/P1-10 后，这个 App 可以长期常驻；在此之前，建议只在你盯着的时候用，或者至少每周看一眼日志里"读请求已发出"和"✓"是否成对出现。

**如果要给一个可靠性分数：当前 6/10；修完 P0 后 8.5/10；再加上 P1 的看门狗可视化和单实例保护，9/10。**

---

## 附录 A：本次审查用到的验证命令

```bash
# 构建与签名
./build.sh
otool -l build/KbKeepAlive.app/Contents/MacOS/KbKeepAlive | grep -A4 LC_BUILD_VERSION   # minos 26.0
codesign -d -r- ~/Applications/KbKeepAlive.app                                            # TCC 依赖的 DR
codesign -dvvv build/KbKeepAlive.app | grep CDHash                                        # 与已安装副本一致

# 内存（不要只看 ps RSS）
footprint -p $(pgrep -f 'KbKeepAlive.app/Contents/MacOS/KbKeepAlive' | head -1)           # 17 MB
vmmap -summary <pid> | grep 'Physical footprint'

# 日志：假保活/断连/状态抖动
grep -c '读请求已发出' ~/Library/Logs/KbKeepAlive/app.log
grep -c '✓ 键盘在线' ~/Library/Logs/KbKeepAlive/app.log
grep -E '断开|连接失败|读失败|蓝牙未开启' ~/Library/Logs/KbKeepAlive/app.log | tail -30
```

复现 `pipefail` 问题：

```bash
zsh -c 'set -euo pipefail; X=$(printf "x\n" | grep -m1 "no-match" | sed -E "s/.*//"); echo REACHED'
# 退出码 1，REACHED 不会打印
```

复现 RunLoop 模式问题（关键结论：`srcDefault=0`，`srcCommon` 正常）：

```swift
// 加载 AppKit 后，把同一个 CFRunLoopSource 分别 add 到 .defaultMode / .commonModes，
// 在 NSEventTrackingRunLoopMode 下 pump 0.3s，打印各自触发次数。
```

## 附录 B：本次实跑日志摘要

- 5442 行，16 次启动，跨 2026-11-14 23:57 → 2026-11-15 12:54
- 11 小时会话：845 次读、60 次断开、844 次"连接中→就绪"（旧构建的状态抖动）
- 同会话每小时自检 RSS：49.1 → 49.0 → 46.4 → 44.0 → 40.4 → 39.1 → 38.2 → 38.0 → 37.3 → 37.3 → 37.2 MB（单调下降，无增长）
- 断连呈严格 10 分钟周期（08:59:39、09:09:43、09:19:46…），App 均在 2 秒内重连成功
- 当前构建（12:36:48 启动）：连续 15+ 次读，每 61 秒一次，电量稳定 100%，无断连；同期 `phys_footprint` 17 MB
- 无 crash report；两次正常退出都有 `=== 退出 ===` 标记
- 统计文件 `stats.json` 42 字节（2013 次，单日），日志 338KB（未触发轮转）

---
---

# 复审（v1.1，2026-11-15 13:00–13:20）

- 复审对象：`Sources/*.swift`（8 个文件，1184 行）、`build.sh`、`Info.plist`，相对上一轮全部改动
- 环境：macOS 26.6.2 / Swift 6.3.3 / arm64；实跑进程 PID 21929（v1.1，Apple Development，CDHash `7113791f…`）
- 方法：全量重读 + 实际构建（`./build.sh` 通过，`-target macos13.0` 无 availability 警告）+ 4 组状态机复刻实验 + v1.1 真实日志（13:05:47–13:15:15）
- 结论先行：**P0 实际修复 3.5/4，P1 实际修复 8.5/10；新引入 2 个 P1、5 个 P2。距离"长期常驻"只差 2 个 P1。**

## R-0. 逐条结论表

| 原编号 | 结论 | 说明 |
|---|---|---|
| P0-1 off 释放链路 | ✅ 已修 | `:142-150` `releaseLink()` + `linkState = .standby`。停扫、cancel、清 `kb/batteryChar/pendingReadAt/connectingSince/nextReadAt` 齐全 |
| P0-2 连接看门狗 | ✅ 已修 | `:213-217` 12s；`:454-470` 发现失败走 `resetLink`；`:289-292` 前置守卫。**但**见 R-2 的 `adopt()` 变体 |
| P0-3 读超时 | ✅ 已修 | `:220-224` 8s 且限定 `.ready`；`:480` `pendingReadAt=nil`；`:481-486` 校验 UUID/长度；`lastReadAt` 移到成功分支。计数拆成 attempt/success/manual，UI 用 success |
| P0-4 重连去抖 | ⚠️ **部分修** | 退避/可取消/去抖都对了，但 `identifier` 守卫**不足以区分"第几代连接"**，存在二次重连（见 **N1**） |
| P1-1 UUID 精确匹配 | ✅ 已修 | `:77-80` 持久化；`:294-298` 优先按 UUID；删掉 `"Keyboard"` 兜底 |
| P1-2 commonModes | ✅ 已修 | `KeyboardMonitor.swift:53` |
| P1-3 扫描过滤 | ✅ 已修（方案被实测否决，替代方案合理） | 见 R-3 专项分析，`withServices: nil` + 退避 + 每秒吸附的组合**没有漏洞**，但有一个暴露的日志缺失 |
| P1-4 清 batteryChar | ✅ 已修 | `adopt/resetLink/releaseLink` 三入口统一 |
| P1-5 限时边界 | ✅ 已修 | `:123-131` 同值续期；`:172-207` ISO8601 绝对 deadline + 过期转 off；`MenuBarController:92-96` 10s titleTimer |
| P1-6 统计健壮性 | ✅ 已修 | `StatsStore:12-17` 容错解码；`:36-42` 坏文件备份；`:88-97` 写成功才清 dirty（且仅在快照未过期时） |
| P1-7 登录项 | ✅ 已修 | `LoginItem:16-20` 异步；`:36-47` requiresApproval；`:83-89` bootstrap 如实返回；`:55-60` XML 转义 |
| P1-8 tap 自愈 | ✅ 已修 | `:16` `CFMachPortIsValid`；`:34-38` 失效重建；`:73` 两种 disabled；`AppDelegate:67-71` 120 次上限；`MenuBarController:118` 打开菜单重试 |
| P1-9 蓝牙权限恢复 | ✅ 已修 | `:236-239` 每秒检查 unauthorized→poweredOn 自动 connect；`btHint` 菜单入口 |
| P1-10 build 证书 | ✅ 已修 | `build.sh:26-42`，固定 CN + `|| true`；实测构建通过并正确打印 DR |
| P2-1 | ✅ | `StatsStore:55-62` 跨零点 key 缓存 |
| P2-2 | ⏭ 有意跳过 | 同意，当前体量无需动 |
| P2-3 | ✅ | `minos 13.0` 已验证；无 availability 警告 |
| P2-4 | ✅ | `DEFAULT_IDENTITY` 固定 + DR 打印 |
| P2-5 | ✅ | `plutil -lint` + `codesign --verify --strict` |
| P2-6 | ✅ | `LSMultipleInstancesProhibited`；版本 1.1/110 |
| P2-7 | ✅ 无需改 | 保留 `.defaultTap`，合理 |
| P2-8 | ✅ | `AppLogger:44-47` os_log 降级 |
| P2-9 | ✅ | `:49` `try write(contentsOf:)` |
| P2-10 | ✅ | `:58-74` 归档失败保留当前文件 |
| P2-11 | ✅ | `flushAndWait()` + `AppDelegate:50` |
| P2-12 | ✅ | 并入 P0-3 |
| P2-13 | ✅ | `:185-189` 档位校验 |
| P2-14 | ✅ | 未用派生属性已删 |
| P2-15 | ✅ | `manualReadCount` 分离 |
| P2-16 | ✅ | `:481-482` |
| P2-17 | ⏭ 有意跳过 | 可接受，但建议补 1 个纯函数测试（见 R-4 建议） |
| P2-18 | ✅ | `currentFootprintBytes()`；实测 12 MB |
| P2-19 | ✅ | `connect():280-288` |

合计：P0 4 条中 3 条完全修好、1 条部分修（N1）；P1 10 条全部有实质修复，其中 1 条（P0-4 的守卫）留了尾巴。

---

## R-1. 复审实测数据

**构建**：`./build.sh` 退出码 0；`minos 13.0`；DR 的 `leaf[subject.CN]` 与上轮完全一致，**TCC 无需重新授权**，与预期相符。

**内存**：`footprint -p 21929` → `phys_footprint: 12 MB`（峰值 12 MB）。比上轮的 17 MB 更低，与"删掉未用属性 + 指标换成 footprint"一致。**无增长迹象。**

**真实恢复路径验证**（这是本轮最有价值的一条正向证据）：

```
13:12:07 扫描 20 秒未发现目标，16 秒后再试      ← 扫描已停，退避 16s，计划 13:12:23 重扫
13:12:12 链路状态 → 连接中                      ← 提前 11 秒！
13:12:12 链路状态 → 已连接 / 保活通道就绪
13:12:13 保活 #1 ✓ 电量 100%
```

从 `13:12:07` 停扫到 `13:12:12` 恢复只隔 5 秒，且**全程没有 `扫描发现` 日志**，可以确定走的是 `tickOnce:248` 的 `systemConnectedTarget()` 每秒吸附分支，而不是扫描。这正是"键盘睡死→用户按键→系统 HID 回连→App 1 秒内吸附"的关键路径，**实测成立**。

**扫描退避行为**：13:05:47 之后日志呈现 `4 → 8 → 16 → 32 → 64` 的递增，无风暴，符合预期。

---

## R-2. 本轮新发现的问题

### N1（P1，对应 P0-4 的残留）`identifier` 守卫挡不住"同一 UUID 的上一代连接"回调，仍会出现二次重连

- **位置**：`Sources/KeepAliveEngine.swift:414-429`（`didFailToConnect`）、`:431-450`（`didDisconnectPeripheral`）、`:342-355`（`resetLink`）
- **问题**：守卫是 `guard peripheral.identifier == kb?.identifier else { return }`。它假设"`kb` 被清空 ⇒ 该外设的回调一定是陈旧的"。但重连场景下 `adopt()` 会把 `kb` 重新设成**同一个 UUID**（`retrievePeripherals` 返回的就是同一个 `CBPeripheral`），于是旧一代连接的迟到回调会重新命中守卫。
- **复刻验证**（模型逐行复刻当前逻辑）：
  ```
  ── B resetLink → 重连 adopt → 迟到 didDisconnect
     resetLink(读超时) backoff=4
     adopt#1                    ← 重连任务已执行
     didDisconnect real -> backoff=8     ← 迟到回调被放行
     -> state=standby kbID=true
  ```
  结果：一代链路被算成两次断连，`reconnectCount` +2、`backoff` 从 4 直接跳到 8，并且会在 `adopt` 之后立刻再调度一次重连（把刚发起的 `central.connect` 覆盖成新任务）。
- **为什么是问题**：退避本意是"链路越不健康、退避越久"，但在"看门狗主动断开 + CB 迟到回调"这种高频组合下，退避会**翻倍过快**，从 2s 直接跳到 8–16s；重连节奏被误判放大，正好抵消了 P0-4 想要的"平滑"。
- **修法**（S 改动）：用**代际号**而不是 UUID 判断，`adopt()` 时自增：
  ```swift
  private var linkGeneration = 0
  private var currentGeneration: [UUID: Int] = [:]   // 或只存一个 (id, gen) 元组

  // adopt() 里：
  linkGeneration += 1
  generationOf[kb.identifier] = linkGeneration
  let gen = linkGeneration

  // 回调里（Node.js 式闭包不好用，改成在 didConnect 时记录）：
  // 更简单的做法：didConnect 时 currentGen = 递增；断连回调里
  guard linkGeneration == generationOf[peripheral.identifier] else { return }
  ```
  更省事的等价做法：在 `resetLink()`/`releaseLink()` 里把"已主动放弃的 UUID"记进一个 Set，回调里 `guard !abandoned.contains(id)` 再清掉——但这在重连成功后要小心清理。**推荐代际号**，语义最清晰。
- **补充**：`didConnect` 目前不校验 `peripheral == kb`（`:408-412`），多 target 时会改错 `targetID`，建议一并加守卫。

### N2（P1）`adopt()` 不检查 `p.state`，命中"系统已连接但本 App 未发现"的外设时白等 12 秒

- **位置**：`Sources/KeepAliveEngine.swift:313-325`
- **问题**：`adopt(p)` 无条件 `central.connect(p)`。当 `connect()` 走分支 (1) `retrievePeripherals(withIdentifiers:)` 或分支 (2) `systemConnectedTarget()` 拿到一个 `state == .connected` 的 peripheral（系统已作为 HID 连着、但本 App 尚未 `discoverServices`），`central.connect()` 对该外设是 no-op，**不会回调 `didConnect`**。
- **后果**：`adopt` 已把 `linkState` 置为 `.connecting`、`connectingSince = now`，于是界面停在"连接中"，**要等 12 秒看门狗才 `resetLink` 重来**。每次遇到这种状态都要白等 12 秒，而用户的感受是"点了恢复保护但半天没反应"。
- **修法**（S 改动）：
  ```swift
  private func adopt(_ p: CBPeripheral) {
      central.stopScan(); scanStartedAt = nil; cancelReconnect()
      kb = p; p.delegate = self
      batteryChar = nil; battery = -1; pendingReadAt = nil
      if p.state == .connected {
          // 系统已经连着：跳过 connect，直接发现服务
          linkGeneration += 1
          linkState = .connecting; connectingSince = Date()
          p.discoverServices([Self.batteryService])
          return
      }
      linkGeneration += 1
      linkState = .connecting; connectingSince = Date()
      central.connect(p, options: nil)
  }
  ```

### N3（P2，新）`didConnect` 未校验 `peripheral == kb`

- 位置：`:408-412`。若 `adopt` 过多个外设（或迟到 `didConnect`），会对非当前目标 `discoverServices` 并覆盖 `targetID`。建议加 `guard peripheral == kb else { central.cancelPeripheralConnection(peripheral); return }`。

### N4（P2，新）`scanBackoff` 只在 `noteLinkHealthy()` 重置，且 `scanStartedAt` 在退避期间为 nil

- 位置：`:225-233`、`:376-380`
- 实测：只要**成功过一次**，`noteLinkHealthy()` 会把 `scanBackoff` 归零，下次从 4s 起步，行为正确。
- 但有一个小效应：扫描退避期间 `scanStartedAt == nil`，此时 `tickOnce` 的 `systemConnectedTarget()` 仍在每秒跑（这是好事，见 R-3），只是 `linkState` 仍为 `.searching`，菜单显示"寻找键盘"而实际在等重连定时器。属于展示层语义偏差，建议**退避等待期间显示"等待重连（Ns）"**而不是"寻找键盘"。

### N5（P2，新）`systemConnectedTarget()` 每秒 2 次 CoreBluetooth 查询

- 位置：`:248`（tick 内）+ `:300`（`connect()` 内）+ `:258`（`retrieveConnectedPeripherals`）
- `retrieveConnectedPeripherals` 是纯内存查询不触发射频，但每次要跨到 `bluetoothd`。空闲（键盘不在）时每秒 2 次，长期常驻下是持续的小功耗。**实测 footprint 无增长、无 fd 增长**，可接受；若要更省，可改成 2–3 秒一次只在 `searching` 状态轮询，或只在"距上次系统 HID 事件"未命中时降低频率。

### N6（P2，新）`resetLink()` 未停扫

- 位置：`:342-355`。`resetLink` 由看门狗触发时 `linkState` 通常不是 `.searching`，一般没有扫描在跑，风险低；但 `didDisconnect` 路径若恰好在扫描中，会残留扫描。建议 `resetLink` 开头补 `central?.stopScan(); scanStartedAt = nil`，与 `releaseLink` 保持一致。

### N7（P2，新）辅助功能授权轮询停止后，只靠"打开菜单"这一个自愈入口

- 位置：`AppDelegate:67-71`（120 次后停止）、`MenuBarController:118`（打开菜单时重试）
- 核对：`scheduleAuthPoll()` 只在校启动时被调用一次（`:30`），`authPollCount` 不会跨会话累积，**原稿把这里写成"多次调用共享计数"是错的，已更正**。
- 真实缺口：轮询在 10 分钟后停止，之后唯一的自愈入口是用户打开菜单（`MenuBarController:118`）。如果用户从不打开菜单，只是在系统设置里授予了辅助功能权限，按键计数会一直不恢复，且菜单栏没有任何提示（`axHint` 只在菜单里可见）。属于低频场景，P2。
- 建议：菜单栏图标在"未授权"时可加一个小角标，或把轮询上限从 120 次放宽到 360 次（30 分钟），成本几乎为零。

---

## R-3. 专项：P1-3"无过滤扫描 + 退避 + 每秒系统连接轮询"组合的漏洞评估

你说得对：**按服务 UUID 过滤在这里不可行**。我的原建议忽略了一个前提——`scanForPeripherals(withServices:)` 的服务过滤依赖广播包里的 **Service UUIDs 字段**（AD type 0x02/0x03），很多廉价 BLE 键盘（包括这把 M150K-1）只在广播里放 Flags + Local Name（AD type 0x01/0x09），不广播 1812/180F。过滤后 CoreBluetooth 根本不会把这个设备上报给 delegate，`.didDiscover` 永远不触发。**我撤回原建议，你的替代方案是对的。**

当前组合的三条路径逐一评估：

| 路径 | 触发条件 | 延迟 | 结论 |
|---|---|---|---|
| ① 扫描命中 | 键盘在广播（蓝牙刚开机、刚配对完、没被系统占用） | ≤ 20s + 退避 | 正常 |
| ② 每秒 `systemConnectedTarget()` 吸附 | 键盘作为系统 HID 已连（睡醒按键、系统自动重连） | **≤ 1s** | 已实测成立（13:12:12） |
| ③ `retrievePeripherals(withIdentifiers:)` 直连 | `targetID` 已持久化且 CB 仍认得 | 立即 | 正常 |

**发现的漏洞**：路径 ② 的命中**不打日志**（`adopt` 里没有日志，只有 `linkState` 变化会打）。实测 13:12:12 那条恢复路径，日志里完全看不出是"扫描发现"还是"系统连接吸附"，只有连续 3 条状态变更。对排查和用户信任都不利。建议在 `adopt` 加一行：

```swift
AppLogger.shared.info("采纳目标 \(p.name ?? "?")（来源：\(source)）")  // source: uuid/system/scan
```

**另一个真实缺口（P2G）**：若键盘**从未被本 App 连过**（`targetID` 为空）且系统也没有把它当 HID 连着（例如刚开机、系统还没自动重连），那么只能走路径 ①，此时依赖 `peripheral.name` 或 `advertisementData[localName]`。实测日志里 `扫描发现` 出现过 1 次（v1.0 时期），说明这条路能走通；但如果某次广播里 name 为空，就会陷入 20s 扫描循环。当前的退避（4→300s 封顶）已经把它从"风暴"降级为"慢"，可以接受；建议再加一个"扫描循环超过 N 次就告警/提示重新配对"的可观测点。

**结论**：三条路径覆盖了"广播中 / 系统已连 / 已知 UUID"三种状态，没有死锁。组合**逻辑上完整**，可长期运行；唯一建议是给路径 ② 加日志、给路径 ① 的长期失败加告警。

---

## R-4. 本轮改动的其他确认

**编译**：`-target arm64-apple-macos13.0` 下全部源码 `-typecheck` 无 availability 警告（`beginActivity` 10.9+、`SMAppService` 13.0+、`CFMachPortIsValid` 全版本、`FileHandle.write(contentsOf:)` 10.15+）。

**并发**：新增的 `DispatchWorkItem` 重连调度、`lastCBState`、`authPollCount`、`titleTimer` 全部只在主线程读写（`dispatchPrecondition` 已在 `start()` 加上 `:111`），未引入竞争。`LoginItem.apply()` 改到全局队列后只写日志、不触碰主线程状态，正确。`beginActivity` 持有并 `endActivity` 配对，无泄漏。

**暂停点（强制要求，无需修改）**：`titleTimer` 是常驻的 10 秒定时器，`housekeepingTimer` 30 秒，`engine.tick` 1 秒——三个常驻定时器的 handler 都只做轻量赋值，可接受。

**P2-17（测试框架）**：同意不引入。但如果哪天要修 N1，建议先把"重连状态机"抽成无 CB 依赖的纯函数（输入当前状态 + 事件，输出动作），用 `swift test` 或一个 20 行的命令行程序跑完上面那张场景表——这次 N1 就是单纯靠"人读"才能发现的，有表会快很多。

---

## R-5. 复审后评分与结论

**评分：当前 8/10（上轮 6/10，目标 9/10）。**

已完成的部分质量很高：
- P0 的 4 个"静默失效"全部有对应看门狗，且都实测过；
- 退避、可取消重连、去抖让 11 小时会话里那种 844 次的重连风暴不可能再出现；
- 恢复路径（睡醒按键 1 秒吸附）是实打实验证过的；
- 内存 12 MB、TCC 不丢、构建脚本修好且加了校验；
- 我原先给的 P1-3 建议被实测否决后，你给出了可用的替代方案——这是本轮最好的一条工程判断。

**离长期常驻还差 2 个 P1**：
1. **N1**：`identifier` 守卫不足以区分连接代际，会造成退避翻倍和二次重连；
2. **N2**：`adopt` 不检查 `p.state`，命中"系统已连但未发现"的外设会白等 12 秒。

两者都是 S 级改动（合计约 20 行），修完即可认为**可以长期常驻**。修之前，当前版本已经比 v1.0 可靠得多，可以日常挂着用；最坏情况是被断连时多等 10 秒左右，不会崩溃、不会静默保活失效（因为有 P0-2/P0-3 的看门狗兜底）。

**结论：修完 N1 + N2 后可长期常驻，评分可到 9/10。**

---

# 终验（v1.2，2026-11-15 13:20–13:40）

- 范围：只核对 N1–N7 的改动与 R-3 的两条建议，不全量重审
- 方法：重读 `KeepAliveEngine.swift` 全部改动段 + 实际构建 + 3 组时序复刻实验 + v1.2 实跑日志
- **结论先行：N2–N7 全部修对。但 N1 的 `.connecting` 代理判断引入了一个 P0 级回归（永久卡死），`didDisconnect` 必须再改 3 行。因此这一轮不建议提交推送。**

## T-0. 逐条结论

| 项 | 结论 | 说明 |
|---|---|---|
| **N1** 代际串扰 | ❌ **没修对，引入 P0 回归** | 判据本身能挡住迟到回调（方向对），但**执行顺序错了**：清理在守卫之前，把 `connectingSince` 清空，导致"本代合法断连"被挡后看门狗也不触发 → `linkState` 永久卡在 `.connecting`。详见 T-1 |
| **N2** `p.state == .connected` | ✅ 修对 | `:335-340` 跳过 no-op connect 直接 discoverServices。`targetID` 移到 `adopt`（`:332`）**没有副作用**，见 T-2 |
| **N3** `didConnect` 目标校验 | ✅ 修对 | `:429-432` `peripheral === kb`；非当前目标直接 cancel。用 `===`（对象同一性）而非 `==`，正确 |
| **N4** `.waiting` 状态 | ✅ 修对 | `:23` 新枚举值；`:229-241` 退避时置 `.waiting`；`:256` 吸附在两种状态都生效。`MenuBarController` 无需改（走 `rawValue` 兜底显示"等待重连"），`refreshTitle` 走"非 ready → ⚠︎"，语义正确 |
| **N6** `resetLink` 停扫 | ✅ 修对 | `:361-362` |
| **N7** 轮询上限 | ✅ 修对 | `AppDelegate:67` 120 → 360 |
| **R-3①** 来源日志 | ✅ 已加且实测 | 日志 `13:19:22 采纳目标 M150K-1（来源：uuid）`，UUID 直连路径实测成立 |
| **R-3②** 扫描长失败告警 | ✅ 修对 | `:234-237` `scanCapWarned` + `noteLinkHealthy` 重置（`:399`） |
| **N5** 每秒 CB 查询 | ⏭ 有意跳过 | 同意，已标注可接受 |

实测：`./build.sh` 通过，DR 证书 CN 未变；v1.2 启动后 1 秒内 uuid 直连 → 通道就绪 → 保活 #1 ✓，footprint 10–11 MB。

---

## T-1. N1 的 P0 回归（必须修，3 行）

### 问题

`didDisconnectPeripheral` 的执行顺序是：

```swift
// :457-469 现状
func centralManager(_ central: CBCentralManager, didDisconnectPeripheral peripheral: CBPeripheral, error: Error?) {
    batteryChar = nil
    battery = -1
    pendingReadAt = nil
    connectingSince = nil          // ← ① 无条件清理，在守卫之前
    guard mode != .off else { ... }
    guard peripheral.identifier == kb?.identifier else { return }   // ← ②
    if kb?.state == .connecting { return }                          // ← ③ 新增代理判断
    ...
}
```

①②③ 的顺序导致：③ 判定"这是上一代迟到回调，忽略"时，① **已经把本代连接的 `connectingSince` 清空了**。

### 复刻验证（逐步打印）

```
事件：connect 在途时收到 didDisconnect
  执行 :458-461 清理 → connectingSince = nil
  :469 .connecting 挡住 → return
12 秒后 tickOnce 看门狗检查：
  linkState == .connecting  = true
  connectingSince != nil    = false        ← 关键
  看门狗触发                = false
  ❌ 永久卡在 .connecting
```

### 为什么无法自愈（这是它比普通 bug 严重的地方）

卡死后同时满足以下条件，**所有恢复路径全部失效**：

| 恢复路径 | 为何失效 |
|---|---|
| 看门狗 1（12s 连接超时） | `connectingSince == nil`，守卫 `let t = connectingSince` 失败 |
| `connect()` 的 `:298` 守卫 | `kb.state == .connecting` → 直接 `return` |
| 每秒 `systemConnectedTarget()` 吸附（`:256`） | 只对 `.searching`/`.waiting` 生效，此时是 `.connecting` |
| 扫描退避看门狗（`:229`） | 同样只对 `.searching` 生效 |
| 用户操作 | 只能手动切模式（触发 `setMode` → `connect()`，但 `:298` 又挡住）或重启 App |

**唯一的幸存路径是切到 `.off` 再切回来**（`releaseLink` 会清 `kb`）。对"防休眠"工具来说这就是**静默永久失效**，且菜单只显示"连接中"，用户不会察觉。

### 触发条件与概率评估（低概率，高后果）

需要 `kb.state == .connecting` 时收到 `didDisconnect`（而非 `didFailToConnect`）。真实可发生的场景：

- App 冷启动 / 切模式 → `adopt()` 发 connect → 键盘此刻刚好被别的设备抢占或自救断开，CB 走 `didDisconnect`
- 连接过程中键盘进入深度睡眠
- 系统蓝牙栈在连接阶段抖动

历史日志里 `didFailToConnect`（"连接失败"）出现 0 次、`键盘断开` 62 次（都在 ready 之后），**当前这份日志没有踩到**；但 62 次断连说明这条链路本身就是高频断开型，随机抖动足够让其发生。**不能因为"目前没复现"就放行。**

### 修法（3 行，两个方案任选）

**方案 A（推荐，最小改动）**：把 ① 的无条件清理拆分，`connectingSince` 只在真正处理断连时才清：

```swift
func centralManager(_ central: CBCentralManager, didDisconnectPeripheral peripheral: CBPeripheral, error: Error?) {
    guard mode != .off else {
        batteryChar = nil; battery = -1; pendingReadAt = nil; connectingSince = nil
        linkState = .standby
        return
    }
    guard peripheral.identifier == kb?.identifier else { return }   // 陈旧回调，不动任何状态
    if kb?.state == .connecting {
        // 本代连接在途时被断开：忽略"退出重连"语义，但必须让看门狗能兜底
        connectingSince = Date()     // ← 刷新计时，12s 后看门狗 resetLink
        return
    }
    batteryChar = nil; battery = -1; pendingReadAt = nil; connectingSince = nil
    kb = nil
    ...
}
```

**方案 B（更彻底）**：回到代际号方案，判据不依赖 `kb.state`：

```swift
private var linkGeneration = 0
private var currentGen: [UUID: Int] = [:]

// adopt(): linkGeneration += 1; currentGen[p.identifier] = linkGeneration
// didDisconnect/didFailToConnect:
guard let gen = currentGen[peripheral.identifier], gen == linkGeneration else { return }
// 再用"本代是否已进入下一阶段"判断，而不是 CB 的 state
```

**验证过的修法效果**（复刻）：`connectingSince` 刷新后，`t=12s 看门狗 fired=true`，系统可在 12 秒内自愈。

### 顺带确认：`didFailToConnect` **没有**中招

`:437-443` 的清理语句在守卫**之后**，所以被 `.connecting` 挡住时 `connectingSince` 保留，看门狗能兜底。**只有 `didDisconnect` 因为清理前置而中招。** 修的时候注意不要把这个已正确的顺序改坏。

---

## T-2. N2 的 `targetID` 挪动：确认无副作用

逐路径核对了"`adopt` 立即写 `targetID`"的影响：

| 情形 | 结果 |
|---|---|
| `adopt` 后 `connect` 失败（`didFailToConnect`） | `targetID` 已写入该外设 → 下次 `connect()` 走分支 1 直接按 UUID 重试，比扫描**更快**，且 UUID 就是 M150，不会连错设备。✓ |
| `adopt` 后链路正常 | 与旧版（`didConnect` 才写）行为一致，只是提前了一个往返。✓ |
| 扫描到名字含 `M150` 的冒名设备 | 属既有的名字匹配风险，与本次挪动无关。✓ |
| `:429` 的 `didConnect` 校验 | `targetID` 在 `adopt` 已写，`didConnect` 不再依赖它，两者不冲突。✓ |
| 重启后 `restoreMode` | 不读 `targetID`，由 `connect()` 分支 1 消费，语义正确（实测 13:19:22 走的就是这条）。✓ |

**结论：N2 没有副作用，`p.state == .connected` 分支的注释也写清了原因。**

---

## T-3. N4 的一个小观察（P3，不阻塞）

- `:233` 设置 `.waiting` 后，`scheduleReconnect(after: scanBackoff)` 到期会调 `connect()` → 如果仍未找到目标，`:314` 置回 `.searching` 并重新扫描，**同时 `scanStartedAt = Date()`**。这个循环是正确的。
- 但 `.waiting` 期间菜单显示"等待重连"，**没有显示还要等多少秒**。`secondsUntilNextRead` 在非 ready 时返回 nil，所以拿不到等待时长。若要更友好，可在 `.waiting` 时用 `reconnectItem` 的调度时刻推算剩余秒数展示。属锦上添花，不影响正确性。

---

## T-4. 最终结论与评分

> **本节已被下方 T-5 取代（T-1 修复后）。** 保留原文以备追溯当时的评审依据。

**（T-1 修复前）评分：7.5/10**（上一轮 8/10，因为 N1 引入了新的 P0 回归而**下调**）。

- N2–N7 与 R-3 两条建议全部修对，质量好：`adopt` 双路径处理、`===` 对象校验、`.waiting` 状态语义、来源日志、扫描上限告警，都是对症的。
- N1 的**判据方向是对的**（用 `kb.state == .connecting` 推断"这是上一代回调"在覆盖的场景下确实有效），但**落地时执行顺序错了**，把一个"退避翻倍"的 P1 升级成了"永久卡死"的 P0。
- 我上一轮推荐的是"代际号"，你选择了更轻的判据。这在架构上是合理取舍（CB 回调不带代际号，`kb.state` 是能拿到的最强信号），**问题不在选择，而在实现顺序**。修 3 行即可。

**（T-1 修复前）不建议推送。** 修完 T-1（方案 A 或 B）后可复查这 3 行并推送，评分回到 **8.5–9/10**。

补一句风险提醒：这个回归恰好属于"低概率、高后果"类别，日常几天可能都触发不到，但一旦触发就是无人值守的静默永久失效——正是前几轮一直在消灭的模式。建议修完后把 `didDisconnect` 在 `.connecting` 阶段的路径单独加一行日志（如"连接过程中被断开，看门狗将重试"），以后出问题能一眼定位。

---

# 放行核验（v1.3，T-1 修复后）

- 范围：只复核 `didDisconnectPeripheral` 这一个函数是否修对
- 方法：读实际部署代码（与描述逐行一致）+ 构建 + 5 路径穷举 + CB 投递顺序推演 + 实跑日志

## T-5. T-1 修复核验结论：✅ 修对，无新问题，**放行**

修复后的执行顺序：

```swift
batteryChar = nil; battery = -1                  // 只留下这两个无害清理
guard mode != .off else { linkState = .standby; return }
guard peripheral.identifier == kb?.identifier else { return }
if kb?.state == .connecting {                    // ……早退分支
    pendingReadAt = nil
    connectingSince = Date()                     // ✅ 刷新而非清空
    AppLogger.shared.info("连接发起期间收到上一代断连回调，已忽略（12 秒看门狗兜底）")
    return
}
pendingReadAt = nil; connectingSince = nil; kb = nil   // ✅ 只在真正处理时清
// 退避 + scheduleReconnect（原逻辑不变）
```

**5 条路径穷举结果**（模型逐行复刻）：

| 路径 | 结果 | 判定 |
|---|---|---|
| S1 上一代迟到回调（你修的场景） | 走早退分支，`connectingSince` 刷新 → `t=12s watchdog fired=true` | ✅ P0 已消除 |
| S2 正常断连（ready 后） | 正常处理 → 退避 4s | ✅ 不变 |
| S3 陈旧回调（`kb == nil`） | identifier 挡住，**不动任何状态** | ✅ 正确（避免误伤后续 adopt） |
| S4 off 模式断连 | → standby，不拉起重连 | ✅ 不变 |
| S5 `kb.state == .disconnected/disconnecting` | 正常处理 → 退避 | ✅ 不变 |

**关键点确认**：

1. **`connectingSince = nil` 只在守卫全部通过后执行** —— P0 回归的直接成因已消除。
2. **早退分支刷新 `connectingSince = Date()`** —— 看门狗从此刻重新计时，12 秒后必 fire，链路可自愈。复刻验证 `fired=true`。
3. **早退分支同时清 `pendingReadAt`** —— 残留读作废，避免读看门狗在 `.connecting` 状态下留着脏时间戳。正确。
4. **顶部只留 `batteryChar = nil; battery = -1`** —— 这两个无状态可破坏，`adopt` 本来也会清，无害。而且它保证"任何断连后 UI 不再显示旧电量"，语义上反而更好。
5. **`didFailToConnect` 未动** —— 已确认其清理仍在守卫之后，本来就无此问题，保持原状正确。
6. **加了定位日志** —— 把"低概率"路径变成日志可见，正是我建议的那条。

**CB 投递顺序补充推演**（确认不存在漏网窗口）：

同一 peripheral 的回调在 central 的 queue 上串行投递。`resetLink()` 的 `cancelPeripheralConnection` 在前、`adopt()+connect()` 在 2 秒后，所以"旧一代 `didDisconnect`"必然**先于** re-adopt 被投递。它到达时只有两种可能：`kb == nil`（S3，被挡住）或 `kb.state == .connecting`（S1，早退+刷新）。**"旧一代 didDisconnect 在新一代 ready 之后到达"在有序投递下不可达**，所以不依赖代际号也是安全的。

## T-6. 最终放行结论

**评分：9/10 —— 可以长期常驻，我确认放行提交推送。**

从最初版本到现在的关键指标对比：

| 维度 | 首轮 review | 现在 |
|---|---|---|
| P0 静默失效 | 4 个（连接卡死、读无超时、off 不放手、重连风暴） | 0 个，全部有看门狗兜底 |
| P1 | 10 个 | 0 个待修 |
| 内存 | 实测 17 MB | 实测 10–11 MB，无增长 |
| TCC | 固定证书方案有效 | 有效，DR 的证书 CN 全程未变 |
| 重连 | 固定 2s、不可取消 | 指数退避 2→60s、可取消、可合并、带去抖 |
| 断连恢复 | 无看门狗，可能卡死 | 12 秒看门狗 + 8 秒读看门狗 + 每秒 HID 吸附（实测 1 秒） |
| 构建 | 无证书时脚本直接失败 | 固定证书 + 校验 + 失败回退 + `plutil`/`codesign --verify` |

**剩余可选项（不阻塞放行）**：T-3 提到的 `.waiting` 状态下显示"还需等待多久"（P3，纯体验）。

唯一需要提醒的运维事项：这次的 P0 回归说明"低概率路径"确实需要日志可见性才能守住。已有的 `连接发起期间收到上一代断连回调` 和 `距上次成功保活 N 秒，期间链路可能假死` 两条日志很好，长期挂机时建议偶尔 grep 一下日志里的 WARN。
