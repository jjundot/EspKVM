# 实施状态

本文件区分已落地代码、自动验证和待完成内容，不替代两份设计基线。

## 已落地

- 唯一 ek.proto/ek.options；Go/nanopb 构建期生成；Go 生成进程锁与原子替换。
- 根 CMake/Ninja、host-only 和 WSL 预设；Windows 构建入口；host CI。
- C/Go 12B 共享帧头、CRC32、528B 有界串口解析及候选超时重同步。
- Go 字段/方向校验、握手双方认证、原始 Challenge transcript、AES-GCM 和严格 seq。
- Go 单 writer、128 队列、输入超龄/写期限、独立心跳看门狗、generation 释放接口。
- 串口单请求事务、随机 request_id、应答关联、总期限；CLI 共用串口库。
- 客户端配置原子保存、Linux 权限及 Windows ACL；先保存 pending 再下发配置，认证读取新配置后确认。
- 固件配网引导源码：UART 配网服务、字段校验、NVS 双槽版本/CRC、恢复出厂 tombstone。应用当前仅启动配网，不会开放输入控制。
- 固件编译、从 IDF 实际元数据生成自包含烧录包、SHA256 校验及 Windows 烧录脚本；IDF CI。

## 自动验证

C/Go 共用帧向量、CRC、嵌入 Magic、字节拆包、坏 CRC 后嵌套候选恢复、超时、GCM 篡改/重放、输入边界、空释放存在性、配置应答丢失不重发、pending 持久化及 Linux 权限、内存 TCP 双向握手/释放、队满关闭。

首次 host 构建、Go race/vet、Windows CLI 交叉编译和约 24 万次串口 fuzz 在 WSL ext4 快照执行。无修改构建、options 触发生成且内容相同时不改生成文件、再次构建不重跑生成器均通过。设备配网固件已用 ESP-IDF v5.2.3 编译，根 CMake 的 device-only 构建与烧录包生成已通过；没有实际烧录。Windows ACL、串口硬件行为、NVS 断电恢复仍需实机验收。

## 仍需实现，不能视为完整可用产品

1. M0 Windows 钩子/Raw Input/回注和 Linux evdev/grab/uinput/热插拔实验及恢复验收。
2. TinyUSB 三接口、Boot/NKRO、USB 完成回调、128 FIFO、独立释放屏障。
3. 固件 WiFi 静态 IP、TCP server、mbedTLS 握手/GCM、单活动会话及独立看门狗。
4. C 消息结构/方向校验和跨语言完整 Protobuf/密码学向量。nanopb 固定长度字段必须额外检查 wire 中必需字段存在性。
5. 主机平台输入、键码映射、多设备生命周期、统一 dispatcher/热键状态机、鼠标分段合并。
6. 常驻 espkvm、完整 Web UI、Host/Origin/token/WS 防护、TCP 配置服务、pending 恢复选择界面。
7. 完整固件第三方依赖锁、安装卸载及发布故障注入；现有 nanopb runtime 固定到 0.4.9.1 的 commit。
8. 设计第 11.1 节全部实机验收和性能基线。

当前 CLI `serial-set` 的配置 JSON 使用 protobuf JSON：bytes/PSK 为 base64，IPv4 字段为 uint32 数值。串口不自动生成 PSK，用户需提供随机 16B 值；CLI `new-psk` 输出 hex，转换后写入配置文件。保存成功仍须 `verify`，不能等同于新 WiFi 可达。
