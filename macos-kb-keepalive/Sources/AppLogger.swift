import Foundation
import os

/// 文件日志：串行队列写入，超过 512KB 自动轮转（保留一份 .1 备份，磁盘占用上限 ~1MB）
/// 句柄失效/写失败时降级到 os_log，不让日志静默丢失
final class AppLogger {
    static let shared = AppLogger()

    let dirURL: URL
    private let fileURL: URL
    private let archiveURL: URL
    private let maxBytes = 512 * 1024
    private let queue = DispatchQueue(label: "com.ming.kbkeepalive.log", qos: .utility)
    private var handle: FileHandle?
    private var pendingBytes = 0

    private init() {
        let base = FileManager.default.urls(for: .libraryDirectory, in: .userDomainMask)[0]
        dirURL = base.appendingPathComponent("Logs/KbKeepAlive", isDirectory: true)
        try? FileManager.default.createDirectory(at: dirURL, withIntermediateDirectories: true)
        fileURL = dirURL.appendingPathComponent("app.log")
        archiveURL = dirURL.appendingPathComponent("app.log.1")
        queue.async { self.openFile() }
    }

    private func openFile() {
        if !FileManager.default.fileExists(atPath: fileURL.path) {
            FileManager.default.createFile(atPath: fileURL.path, contents: nil)
        }
        handle = try? FileHandle(forWritingTo: fileURL)
        _ = try? handle?.seekToEnd()
        pendingBytes = Int((try? handle?.offset()) ?? 0)
    }

    func info(_ msg: String)  { write("INFO", msg) }
    func warn(_ msg: String)  { write("WARN", msg) }

    /// 退出前调用：等串行队列里未写完的日志落盘
    func flushAndWait() { queue.sync {} }

    private func write(_ level: String, _ msg: String) {
        queue.async {
            let line = "\(Self.stamp()) [\(level)] \(msg)\n"
            guard let h = self.handle else {
                os_log("%{public}@", type: level == "WARN" ? .error : .info, line)
                return
            }
            do {
                try h.write(contentsOf: Data(line.utf8))
                self.pendingBytes += line.utf8.count
                if self.pendingBytes > self.maxBytes { self.rotate() }
            } catch {
                os_log("日志写入失败: %{public}@", type: .error, String(describing: error))
            }
        }
    }

    private func rotate() {
        handle?.closeFile()
        handle = nil
        try? FileManager.default.removeItem(at: archiveURL)
        do {
            try FileManager.default.moveItem(at: fileURL, to: archiveURL)
        } catch {
            // 归档失败：保留当前文件继续追加，避免丢日志
            os_log("日志轮转归档失败: %{public}@", type: .error, String(describing: error))
        }
        if !FileManager.default.fileExists(atPath: fileURL.path) {
            FileManager.default.createFile(atPath: fileURL.path, contents: nil)
        }
        handle = try? FileHandle(forWritingTo: fileURL)
        _ = try? handle?.seekToEnd()
        pendingBytes = Int((try? handle?.offset()) ?? 0)
    }

    private static let fmt: DateFormatter = {
        let f = DateFormatter()
        f.dateFormat = "yyyy-MM-dd HH:mm:ss"
        return f
    }()
    private static func stamp() -> String { fmt.string(from: Date()) }
}

/// 当前进程真实物理内存（字节）：phys_footprint 不含 AppKit/CoreBluetooth 的
/// 共享只读页，比 RSS 更接近"这个 App 占了多少内存"的直觉
func currentFootprintBytes() -> Int {
    var info = task_vm_info_data_t()
    var count = mach_msg_type_number_t(MemoryLayout<task_vm_info_data_t>.stride / MemoryLayout<natural_t>.stride)
    let kr = withUnsafeMutablePointer(to: &info) { ptr in
        ptr.withMemoryRebound(to: integer_t.self, capacity: Int(count)) { intPtr in
            task_info(mach_task_self_, task_flavor_t(TASK_VM_INFO), intPtr, &count)
        }
    }
    return kr == KERN_SUCCESS ? Int(info.phys_footprint) : 0
}
