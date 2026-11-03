# 实施状态（2026-11-03）

唯一行为基线是 [v11 决议](esp32-kvm-decisions.md)；线格式以 ek.proto/ek.options 为准。下述“通过”只指列明的自动测试或构建，不代表硬件验收完成。

## 已实现

- 协议 v2 候选：optional 凭据、six_kro、带 id 心跳、绑定 owner/接管意图的双方认证、GCM、严格方向和字段长度检查。
- ReleaseAll/Released：三个 HID 接口全释放完成才确认；调用方 500 ms 有界等待，关闭及 writer 异常唤醒全部等待者。首次引入该确认是为了消除“写出后立即转发，但固件仍在释放中”的接口冲突。
- 正式固件：静态 IP WiFi STA、TCP、四个静态上下文（一活动会话及最多三握手）、来源/全局预算、认证后串行接管、独立 25 ms 轮询的 3 s 看门狗、TCP/串口共用配置事务。
- 正式 HID：128 FIFO、完成回调推进、鼠标分段及余量、独立释放状态、pending-release、协议切换重建、SET_IDLE、LED、持久化 6KRO。SET_IDLE 重复报告不推进 FIFO；只有主机在用的接口参与释放屏障，端点 100 ms 不完成即放弃并跳过，迟到完成后补发当前状态（BIOS 只驱动 Boot 键盘时不再卡死）。
- 睡眠目标机：挂起及唤醒总线复位 20 s 宽限内 ReleaseAll 立即确认、状态上报 `usb_suspended`，客户端释放等待放宽到 20 s，切换后输入每秒最多请求一次远程唤醒。
- 配网 UART 日志：ESP_LOG 全部封装为 SerialPacket.log 帧，不再以文本混入；`espkvm ctl serial-log` 查看。
- TCP CFG_SET 成功后半关闭并等待对端读完 CFG_RESULT（最多 200 ms）再重启。
- 配置：凭据锁内合并、脱敏字段缺省、NVS 双槽和 v1 blob 迁移；当前台架构建未配对默认 VID/PID 为 0x046D/0xC52B。串口请求、RX 及解析器已消费区清零。
- Go 客户端：输入状态/引用计数、物理键码映射、热键重武装、异步切换屏障、抑制集合、鼠标合并和分段、多设备独立重连及显式接管。切换中选择 LOCAL 为取消，不关闭会话。
- Linux 捕获：REMOTE 期间热插拔（含蓝牙重连）在全释放边界补抓，拔出只释放该设备按键，SYN_DROPPED 用 EVIOCGKEY 重建，三者都不再断开会话；被其他程序独占（EBUSY）的设备跳过并在切换状态中警告；udev 规则加入 ID_INPUT_KEY。
- 热键：配置的 Usage 组合；Windows 另有默认热键的 Raw Input / RegisterHotKey 兜底及 Ctrl+XButton1/2 正反向轮换，见设计第 8 节。
- 平台输入适配器：Windows LL hooks/Raw Input、标记回注、ClipCursor、桌面/会话切换回退；Linux LOCAL 被动读、SYN_REPORT 批次、全释放边界抓取/复查/确认、SYN_DROPPED、设备移除及轮询热插拔。Linux 正常返回 LOCAL 等待释放边界，故障及退出立即解除抓取。
- 常驻 espkvm 及本地 API：密码登录（默认 admin，配置中存 `md5("espkvm_"+密码)`，旧 PBKDF2 记录登录时迁移）、会话绑定密码摘要（任何进程改密后旧会话失效）、HttpOnly/SameSite cookie、CSRF、Host/Origin；启动凭据仅在默认密码时可用；敏感读取和 fetch SSE 同样要求 CSRF 请求头，SSE 客户端/事件大小上限、脱敏状态；设备管理、排序、热键、串口/TCP 配置和 pending 操作已接线。
- 客户端文件跨进程锁、原子替换与原有权限保护；事务 token 防止旧回调删除新 pending。拒绝码 1/3 自动清理，未知结果及存储失败保留候选。
- Linux 权限规则与安装/卸载脚本；固件复制后哈希校验；module 与仓库路径统一为 github.com/jjundot/EspKVM，Go 工具链基线统一为 1.27.3。

## 已验证

- 2026-11-03：Windows `go vet ./...` / `go test ./...` 通过；WSL 中 8 项 CMake 原生测试（含 ASan/UBSan、HID 引擎新增的 idle/BIOS/stall 用例及 C 会话与 Go 客户端互通）通过；ESP-IDF 6.0.3 正式固件与 M1 固件编译打包成功，未烧录。Linux race 测试本轮未重跑。
- 2026-11-02：Windows Go 全包单元测试与 vet；WSL Linux 全包 race 测试与 vet；Windows/Linux 客户端与平台探针构建通过。
- nanopb/Go Protobuf 字节及 Node 独立 oracle 的 HMAC、密钥、AAD、GCM 密文/tag 向量。
- 同一份 C 网络会话源码通过 POSIX 任务/硬件适配器与真实 Go TCP 客户端互通：认证、脱敏 CFG_GET、释放确认、接管、三个握手槽、非法方向/缺失字段、3 s 看门狗。
- C HID FIFO/分段/释放/重挂/协议切换测试；C 配置凭据合并、拒绝、提交失败、双槽恢复及旧 NVS 迁移测试。
- C 解析器 10,000 组随机碎片/恢复性质测试，及共享向量/噪声/嵌套恢复/超时测试；便携 C 测试在 ASan/UBSan 下通过。另提供 Clang libFuzzer 目标，尚未在本机运行 libFuzzer。
- ESP-IDF 6.0.3 + esp_tinyusb 3.0.0 的正式固件已编译链接，依赖锁已加入源码目录；未烧录。
- espkvm API 模式前台运行两秒并正常退出，禁用输入捕获，未输出启动凭据。后台进程凭据交换烟测被自动审批拒绝，未执行；HTTP 认证流程由测试覆盖。
- Windows 被动探针运行 15 秒并退出，计数全为零；随后 F24 回注返回 Access is denied，0/2 成功。探针已改为回注失败非零退出，详见 [原始记录](../experiments/windows-input/results/2026-11-02.md)。不能据此判定真实输入或阻断实验通过。
- Linux 探针严格编译通过；当前 WSL 无可读 evdev，诊断路径符合预期。

## 未完成及外部条件

1. Web UI 已嵌入 espkvm，仍待用户桌面验收布局、登录链接和串口/设备配置流程。
2. M1 烧录缺少明确串口及具有合法使用依据的 VID/PID，当前串口扫描无设备。不得沿用 Logitech 的默认标识，已有旧 sdkconfig 需显式检查。
3. Windows 实际敲键/移动、吞键后 Raw Input、提权/安全桌面/锁屏恢复；Linux 真实 evdev、抓取/热插拔及多设备行为；全部仍待用户桌面验收。
4. 真实 ESP32-S3 的 WiFi/TCP/GCM、USB/BIOS、释放确认、NVS 断电及故障注入验收；500 ms 释放边界须实测确认。
5. 协议正式冻结和发布、输入到受控机事件的 p50/p95/p99 性能基线仍未完成，见 [验收记录](validation-2026-11-02.md)。

## CLI 与 API 模式

配置 JSON 使用 Protobuf JSON：PSK 为 base64，IPv4 为 uint32；CLI new-psk 输出 hex，使用前转换。首次必须给出 password（开放网络给存在的空字符串）及 16B PSK；已配对时缺省两字段表示保留。serial-set 成功不表示新 WiFi 可达，仍需 verify。

pending-list 不输出凭据；pending-discard 只删除本地恢复数据，不修改设备。守护进程验证 pending 前暂停自己对应的重连循环，避免争抢设备会话；不自动抢占其他 owner。

```sh
espkvm -capture=false -browser=false -bootstrap-file /private/espkvm-launch.txt
```

正式控制页使用默认 `-browser=true` 自动打开登录页。配网和诊断子命令集成在同一程序中：`espkvm ctl ...`；`espkvm ctl -serial COMx [-duration 30s] serial-log` 打印设备日志帧。

启动文件必须不存在，放在用户私有目录。它包含短时、单次 URL fragment 凭据，仅在 Web 密码仍为默认值时可换取会话；不能放到共享目录或日志；退出后删除。
