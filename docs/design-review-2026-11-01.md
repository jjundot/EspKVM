# 2026-11-01 设计修订与硬件优先顺序

本修订覆盖 v10 中对应的行为决议。当前 `ek.proto` 仍是可构建的 v1 草案；optional 凭据、心跳回显及接管字段在 M1 与最小 M0 后集中修改、递增协议版本并更新向量，不把文档决议误记成已实现功能。

## 先打通硬件

顺序改为：USB 三接口测试固件 → 最小 Windows 钩子/Raw Input 实验及 Linux 模式切换实验 → 实机敲字/恢复本机输入 → 冻结新协议 → 会话与完整客户端。普通编译不代替 Windows/Linux/BIOS 实机验收。

M1 使用独立 `experiments/usb-hid` 工程，不开放 UART 输入注入，不依赖 WiFi 或网络协议。构建目标 `hid-selftest` / `rebuild-hid-selftest`，烧录包目标 `package-hid-selftest`。报告编码器放在 `sources/device/components/hid_reports` 供复用；实验中的单生产者同步提交不是正式固件的 FIFO、任务间并发或会话释放屏障实现。

## Linux LOCAL 被动读取

- LOCAL 只读取 evdev，既不 `EVIOCGRAB`，也不把普通输入通过 uinput 回注；客户端退出不影响本机的正常输入路径。
- 正常进入远程先等物理键/按钮全部释放，再在批次边界抓取所有选定设备并重新读取物理状态。多设备抓取不能原子完成，任一失败或检测到竞态就撤销本次全部抓取、留在 LOCAL。切换等待不能阻塞 dispatcher，界面显示等待释放。
- 已在 LOCAL 送到桌面的按键不能靠另一个 uinput 虚拟设备可靠地“撤销”。所以 LOCAL 下可能看见整个切换组合，不只首个 Ctrl；不承诺吞掉完成组合的按下。Windows 同步钩子和 Linux 已抓取的远程模式仍能吞掉完成组合的事件。
- 正常回 LOCAL 先释放远程状态，优先在物理全释放边界解除抓取。故障/退出立即解除抓取，不为等按键释放延误本机恢复；此时本机可能观察到物理保持键的后续事件。抑制集合保证远程转发语义，不宣称能过滤 LOCAL 的原生桌面输入。
- uinput 仅保留给确有需要的切换修复/实验；不得再次变成 LOCAL 普通输入的必经路径。仍处理 SYN_DROPPED、热插拔、逐设备引用计数和程序自建虚拟设备排除。

## 三个握手槽与认证接管

- 保留一个活动会话，允许三个有界握手上下文；每个 accept 起固定总期限 2s，另有单帧期限。accept 不等待网络认证或 USB。
- 限制来源并发和失败频率、全局握手资源预算，满槽立即拒绝；三个槽能缓解串行占槽，不能消除局域网 DoS，更不能用按 IP 限流声称解决多源攻击。
- 只在 PSK 认证成功后进入串行接管仲裁。使旧 session/generation 失效、停止旧输入、触发高优先级释放屏障，再启用新会话；USB 不能交付释放时保持 pending-release，新会话不得输入。未经认证的新连接不影响旧会话。
- 自动重连可替换同一客户端实例/owner 的半开会话；不同 owner 的接管必须是显式用户意图。新版本握手应将 owner 和接管意图绑定到认证证明，不能接受未认证的元数据决定接管。
- 被替换连接尽量收到认证的原因通知后关闭；收到通知的客户端回 LOCAL 并停止自动接管。通知丢失时自动重连也不得无条件抢占别的 owner，避免两个合法客户端互相踢下线。

## 配置字段存在性

新 schema 使用 `optional string password` / `optional bytes psk`：缺省表示保留；显式空 password 表示开放网络；显式 PSK 必须恰为 16B，显式空 PSK 拒绝。首次配置必须提供这两个字段（开放网络提供存在的空 password），不得从脱敏快照凭空补零。

非敏感字段继续完整快照替换。服务在锁内把缺省敏感字段与活动配置合并，再统一校验和原子保存。CFG_DATA/info 不返回这两个 optional 字段，不用“存在的空值”伪装脱敏。修改名称不再要求重新填写密码。pending 比对要忽略敏感字段的存在性，并继续保留改 PSK 前后的候选凭据。

## 心跳回显与指标

新消息使用 `Heartbeat { uint32 id; bool reply; }`。每方向独立生成非零 id；请求 `reply=false`，对端只回一次相同 id、`reply=true`，响应不再回显。每方向最多一个在途探测；未知/重复/过期响应不形成 RTT 样本，连接更换后丢弃旧探测。

RTT 从 writer 开始发送探测到收到并认证匹配响应，用单调时钟计量，记录样本数、窗口和 p50/p95/p99。它包括网络/两端调度，**不是输入→受控机事件延迟，也不是 USB 完成确认**。第 11.1 节端到端基线仍须目标机观测、可对齐时钟或外部采集，独立报告。

## 6KRO 兼容模式

持久化键盘兼容模式。强制模式同时使用标准 Boot Keyboard 报告描述符和 8B 报告，即使主机不发 SET_PROTOCOL 也保持 6KRO；不能只把 NKRO 描述符对应的报告缩成 8B。普通键超过六个时报告 ErrorRollOver。模式修改后重新枚举/重启。

M1 的模式保存在独立 `hid_test` NVS 命名空间，长按测试按钮切换并重启。正式 Config 中的模式字段与 pending 事务在后续 schema 修改时接入；不把实验偏好当成正式配置服务已经完成。

## 本地 UI

改为 SSE 状态推送 + POST 操作，不引入 WebSocket 依赖。SSE 只发脱敏状态，限制事件长度/客户端数，并处理断线恢复；POST 仍做 Host、Origin 和 CSRF 校验。

不再允许任何访问初始页面的进程直接取得写 token。启动生成短时、一次性的启动凭据；以 URL fragment 交给当前用户浏览器，页面立即清除 fragment，并通过同源 POST 换取随机 HttpOnly、SameSite=Strict、Path=/ 的会话 cookie。启动凭据只用一次且过期，不进入 query、日志或普通页面响应。cookie 不取代写请求的精确 Origin/CSRF 防护。

该模型缓解其他本地用户直接 curl 页面取得权限；不是同一 OS 用户下恶意进程的隔离边界，也不把浏览器命令行/历史/扩展视作秘密保险箱。多用户部署仍须每用户私有凭据及启动通道，必要时使用 OS 认证 IPC。

## SDK 基线核实

截至 2026-11-01，官方说明 ESP-IDF 5.2 分支已在 2026 年 8 月 EOL，5.2.8 为最后一次补丁；不再把之前编译成功的 5.2.3 作为新项目基线。

本轮选用已发布补丁版本 **ESP-IDF 6.0.3 + esp_tinyusb 3.0.0**，分别锁定 IDF tag 和组件版本，并提交 M1 的实际组件锁文件。3.0.0 组件声明 IDF >=5.3；这种声明只说明依赖约束允许，实际 S3 编译与实机兼容仍分别验收。6.1 是更新的稳定分支，并非本轮已验证基线；6.0.3 也不特殊宣称为永久 LTS，按官方分支支持期安排升级。

核实来源：

- [官方 5.2.8 发布与 EOL 说明](https://github.com/espressif/esp-idf/releases/tag/v5.2.8)
- [官方 6.0.3 发布](https://github.com/espressif/esp-idf/releases/tag/v6.0.3)
- [官方支持策略](https://github.com/espressif/esp-idf/blob/master/SUPPORT_POLICY.md)
- [esp_tinyusb 3.0.0 依赖约束](https://components.espressif.com/components/espressif/esp_tinyusb/versions/3.0.0/dependencies?language=en)
