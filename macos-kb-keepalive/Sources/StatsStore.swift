import Foundation

/// 按键统计：线程安全计数 + 每日分组，JSON 持久化到 Application Support
/// 内存开销：一个计数器 + 一个小字典（每天一个条目），脏数据每 30 秒落盘一次
final class StatsStore {
    /// 容错解码：缺字段/字段类型变化时取默认值，而不是整体失败清零
    private struct Payload: Codable {
        var total: Int = 0
        var daily: [String: Int] = [:]
        enum CodingKeys: String, CodingKey { case total, daily }
        init() {}
        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            total = (try? c.decode(Int.self, forKey: .total)) ?? 0
            daily = (try? c.decode([String: Int].self, forKey: .daily)) ?? [:]
        }
    }

    private let lock = NSLock()
    private var payload: Payload
    private var dirty = false
    private let fileURL: URL

    // 当日 key 缓存：避免每次按键都过 DateFormatter（输入回调路径上）
    private var cachedDayKey: String?
    private var cachedDayKeyValidUntil = Date.distantPast

    init() {
        let dir = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("KbKeepAlive", isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        fileURL = dir.appendingPathComponent("stats.json")
        if let data = try? Data(contentsOf: fileURL) {
            do {
                payload = try JSONDecoder().decode(Payload.self, from: data)
            } catch {
                // 文件损坏：先备份再重置，避免历史被空数据覆盖后永久丢失
                let backup = dir.appendingPathComponent("stats.json.corrupt-\(Int(Date().timeIntervalSince1970))")
                try? FileManager.default.moveItem(at: fileURL, to: backup)
                AppLogger.shared.warn("stats.json 解码失败（\(error.localizedDescription)），已备份为 \(backup.lastPathComponent) 并重置")
                payload = Payload()
            }
        } else {
            payload = Payload()
        }
    }

    private static let dayFmt: DateFormatter = {
        let f = DateFormatter()
        f.dateFormat = "yyyy-MM-dd"
        return f
    }()

    /// 今天的日期 key（带缓存，跨本地零点自动失效）
    private func todayKey() -> String {
        let now = Date()
        if let k = cachedDayKey, now < cachedDayKeyValidUntil { return k }
        let key = Self.dayFmt.string(from: now)
        cachedDayKey = key
        cachedDayKeyValidUntil = Calendar.current.startOfDay(for: now).addingTimeInterval(86_400)
        return key
    }

    func increment() {
        lock.lock()
        payload.total += 1
        payload.daily[todayKey(), default: 0] += 1
        dirty = true
        lock.unlock()
    }

    var todayCount: Int {
        lock.lock(); defer { lock.unlock() }
        return payload.daily[todayKey()] ?? 0
    }

    var totalCount: Int {
        lock.lock(); defer { lock.unlock() }
        return payload.total
    }

    /// 每 30 秒调用；无变化时不写盘。写失败保留脏标记，下次重试（不会静默丢数据）
    func flush(force: Bool = false) {
        lock.lock()
        guard dirty || force else { lock.unlock(); return }
        let snapshot = payload
        lock.unlock()
        do {
            let data = try JSONEncoder().encode(snapshot)
            try data.write(to: fileURL, options: .atomic)
            lock.lock()
            // 只有成功落盘才清脏标记；期间新增的计数会保留到下次 flush
            if snapshot.total == payload.total { dirty = false }
            lock.unlock()
        } catch {
            AppLogger.shared.warn("统计落盘失败: \(error.localizedDescription)")
        }
    }
}
