# ESP32 键鼠共享器:设计决议汇总 (v11)

> 本文是唯一实施基线，替代 v10 及 2026-11-01 修订。线格式以 `sources/protocol/ek.proto` 和 `ek.options` 为准，协议版本为 2；禁止降级。v2 的代码与向量验收不代替 M1/M0 实机验收，发布协议尚未冻结。

## 1. 项目目标

用 ESP32 开发板作为 USB HID 键鼠模拟器,让一台**非受控电脑**(Windows / Linux)上的真实键盘鼠标,通过 WiFi 控制另一台**受控电脑**。

```
非受控电脑 (Win/Linux)                       受控电脑 (任意 OS)
┌──────────────────────────┐                ┌───────────────────────┐
│ 真实键盘/鼠标             │                │ 识别为 USB 键盘+鼠标    │
│   ↓ 捕获/拦截             │   WiFi / TCP   │        ▲ USB HID      │
│ Go 客户端 (TCP Client)    │ ◄────────────► │  ESP32-S3 (TCP Server) │
│ Web UI 127.0.0.1:62626    │   AES-128-GCM  │                       │
└──────────────────────────┘                └───────────────────────┘
        ▲ 串口(仅首次配网)
        └──── ESP32 UART 口
```

## 2. 决议总表

| 项目 | 决议 |
|---|---|
| 主控芯片 | **ESP32-S3**(原生 USB OTG)。经典 ESP32 / C3 / C6 不能做 USB HID 设备 |
| 固件 SDK | **ESP-IDF 6.0.3 + esp_tinyusb 3.0.0**，固定版本，先验证 M1；5.2 分支已 EOL |
| 开发环境 | **WSL2 编译**(工程放 WSL ext4 内,不放 `/mnt/c`),**Windows 烧录/调试** |
| 客户端语言 | **Go**(Win/Linux 分平台构建验证，标准库 HTTP + SSE/POST，`go:embed` 打包前端) |
| 客户端界面 | Web UI,仅监听 `127.0.0.1:62626` |
| 网络 | 仅 WiFi **Station** 模式,**固定 IP**,无 SoftAP、无 DHCP |
| 传输 | **单 TCP 端口**(默认 24800)。无 UDP、无发现、无扫描 |
| 首次配网 | **USB 串口**,由客户端 Web UI "串口配网"页下发;之后配置走 TCP `CFG_SET` |
| 序列化 | **Protobuf**(固件 nanopb,客户端 protobuf-go),单一事实来源 `ek.proto` |
| 外层帧 | 串口/TCP 共用 12 字节 ek_hdr_t:magic、len、flags、rsvd、seq;均为小端 |
| 加密 | **AES-128-GCM**,每次连接用 PSK 派生新会话密钥 |
| 切换热键 | **Ctrl + Alt + W 同时按下**,Web UI 可改;Windows 另有 Ctrl+鼠标侧键正/反向轮换(第 8 节) |
| 多设备 | 客户端**同时连接多台 ESP32**,但同一时刻只控制一台;同一热键按顺序**循环切换**:本机 → DEV0 → DEV1 → … → 本机 |
| 多设备协议/固件 | 使用同一套单会话固件,不增加跨设备协议。每台设备独立 IP、独立 PSK、独立会话;切换逻辑在客户端 |
| 鼠标 | 仅**相对鼠标**,走 TCP;客户端累加 delta,不排队 |
| 键盘 | 发送**完整状态**(NKRO 位图),不发按键事件 |
| 不做 | 边缘触发、绝对鼠标、剪贴板共享、SoftAP、UDP、端口扫描、TCP 协议重同步 |

## 3. 硬件

- 板子使用 ESP32-S3 开发板(如 ESP32-S3-DevKitC-1)。
- **原生 USB 口** → 受控电脑(同时供电、提供 HID)。
- **UART 口** → 非受控电脑(烧录、首次配网;应用日志以 SerialPacket.log 帧输出,不以文本混入配网协议)。
- USB 复合 HID 接口:
  - 键盘:独立 Boot Keyboard 接口,Report Protocol 使用 NKRO 位图;Boot Protocol 使用标准 8 字节报告。BIOS 兼容性按目标机器验收
  - 相对鼠标:5 键 + 纵向/横向滚轮
  - Consumer Control:多媒体键
- USB 轮询间隔 1 ms。键盘 LED(Num/Caps/Scroll)通过 HID Output Report 读取并上报。
- 键盘接口不使用 Report ID;鼠标和 Consumer 分别使用独立 HID 接口。处理 `SET_PROTOCOL`、`SET_IDLE` 及两种模式的 LED Output Report。Boot 模式超过 6 个普通键时报告 ErrorRollOver,不得任意挑选 6 键。切换协议时清除待发送报告并从当前逻辑状态重建。
- 鼠标 X/Y 为有符号 16 位,有效范围 -32767..32767;纵横滚轮为有符号 8 位,范围 -127..127。按各自范围分段提交并保留余量,不因钳位丢失移动量。

## 4. 网络与配网

### 4.1 串口配网协议(115200,8N1)

串口和 TCP 共用 ek.proto、Config、ConfigResult 及配置校验/持久化服务,不再实现文本命令或 JSON 参数解析。串口采用独立 SerialPacket 信封,不执行 TCP 握手/GCM/每方向 seq 校验规则,不允许输入消息。

串口复用第 5.1 节的同一个 12 字节 ek_hdr_t,传输格式为:

```
ek_hdr_t | Protobuf(SerialPacket)[len] | CRC32_LE[4]
```

串口 flags 必须恰为 EK_FLAG_CRC(0x02),不加密。CRC 使用 CRC-32/ISO-HDLC(IEEE),反射多项式 0xEDB88320、init/xorout 均为 0xFFFFFFFF,校验字符串 123456789 的结果为 0xCBF43926。CRC 覆盖原始 12 字节头及 body,以 4 字节小端附后;len 不包含 CRC。body 最大 512 字节,整帧最大 528 字节。CRC 检测损坏,不提供认证或加密;串口仍以本机 USB 物理访问可信为安全边界。

串口 header.seq 用作 request_id,非零递增,应答原样回填;它不使用 TCP 的每方向严格递增校验,不作为加密 nonce。SerialPacket 不重复存 request_id。客户端最多一个请求在途,只接受当前 seq 的预期应答或错误 ack;打开端口时清空接收缓存并随机选择非零起始 seq,之后递增,到 UINT32_MAX 后关闭并重新打开。request_id 只关联应答,不提供持久去重或 exactly-once 保证。proto_ver 必须为 2。允许消息:

| 请求 | 应答 | 行为 |
|---|---|---|
| info_get | info | 返回固件版本、MAC、paired、脱敏 Config,不返回 PSK/密码 |
| cfg_set | cfg_result | 复用完整 Config 校验及原子保存;成功后按 6.2 节应答并重启 |
| reboot | ack | 先应答后重启 |
| factory_reset | ack | 清除配置,持久化成功后应答并重启;失败返回非零 code |
| — | log | 设备主动输出的一行应用日志(D→C),不应答任何请求 |

合法信封中的不支持消息/版本返回 ack 非零 code,不执行配置。log 帧使用设备自有的非零递增 seq,与 request_id 无关;客户端按消息类型识别并跳过,不作为应答。串口事务和 TCP 配置事务互斥;清除配置或重启均先触发安全释放。factory_reset 仅由用户显式恢复出厂操作触发,不自动用于恢复失败。首次配对 psk 必须是 16 字节,不能用空值创建配对配置。

该 UART 禁止输出普通文本日志及输入回显。固件在 app_main 最先安装 UART 驱动并通过 `esp_log_set_vprintf` 把每行 ESP_LOG 封装为完整的 `ek_hdr_t | SerialPacket{log} | CRC32` 帧(text 最多 255 字节,去掉行尾换行),每帧一次 `uart_write_bytes` 写出,驱动保证它与配网应答帧不交错。`espkvm ctl -serial COMx serial-log` 解析并打印这些日志;配网请求期间收到的日志写入客户端 debug 日志。ROM/Bootloader 及 app_main 之前的 IDF 启动文本仍可能出现,视为噪声并按第 5.2.2 节重同步。客户端超时不得盲目重发 cfg_set/factory_reset;改用状态读取及 pending 恢复流程。手动救援提供 Go CLI,与 Web UI 共用 serialcfg 库。

### 4.2 客户端串口配网流程

选择串口 → info_get 获取默认/现有脱敏配置 → 填写非敏感完整配置及首次必需的凭据 → 生成随机 16 字节 PSK(crypto/rand) → 原子保存 pending 记录 → cfg_set → cfg_result 成功后等待自动重启 → 使用新 IP/PSK 完成 TCP 认证并核对非敏感配置 → 标记 ready。串口库使用 go.bug.st/serial。

应答丢失时保留旧配置和 pending 新配置,先尝试新 TCP 认证或重新读取串口状态,不得重新生成 PSK、自动恢复出厂或覆盖旧设备记录。串口请求总等待期限为 3 s,超时只代表结果未知;串口传输完成不等于持久化成功。

### 4.3 网络参数

未配对 info 默认值：名称 EspKVM，IP 192.168.1.200，网关 192.168.1.1，掩码 255.255.255.0，端口 24800；SSID 为空，VID/PID 为 0，凭据不存在。首次配置必须填写合法网络参数及具有合法使用依据的 VID/PID，默认值不表示可直接启用 USB。

- 固件使用 `esp_netif_dhcpc_stop` + `esp_netif_set_ip_info` 设置静态 IP。
- `esp_wifi_set_ps(WIFI_PS_NONE)` 关闭省电;TCP 开启 `TCP_NODELAY` 与 keepalive;固定信道更佳。
- 客户端只保存设备列表:名称、IP、端口、PSK(`Challenge` 中的 MAC/名称/固件版本仅用于 Web UI 显示,不做校验)。
- 修改 IP 后设备重启,客户端按新 IP 重连。

### 4.4 会话规则

- 保留一个活动会话，允许三个有界握手上下文；每个 accept 起固定总期限 2s，另有单帧期限。accept 不等待网络认证或 USB。
- 限制来源并发和失败频率、全局握手资源预算，满槽立即拒绝；三个槽能缓解串行占槽，不能消除局域网 DoS，更不能用按 IP 限流声称解决多源攻击。
- 只在 PSK 认证成功后进入串行接管仲裁。使旧 session/generation 失效、停止旧输入、触发高优先级释放屏障，再启用新会话；USB 不能交付释放时保持 pending-release，新会话不得输入。未经认证的新连接不影响旧会话。
- 自动重连可替换同一客户端实例/owner 的半开会话；不同 owner 的接管必须是显式用户意图。新版本握手应将 owner 和接管意图绑定到认证证明，不能接受未认证的元数据决定接管。
- 被替换连接尽量收到认证的原因通知后关闭；收到通知的客户端回 LOCAL 并停止自动接管。通知丢失时自动重连也不得无条件抢占别的 owner，避免两个合法客户端互相踢下线。

- 心跳双向每 1 s 一次，独立看门狗在最后完整认证有效帧 3 s 后断开并触发释放；碎片、无效帧不能续期。未配对设备不开放输入。

## 5. 协议

### 5.1 串口/TCP 共用外层帧头(小端,12 字节)

```c
#define EK_MAGIC      0x4D564B45u  /* 字节序列 'E','K','V','M' */
#define EK_HDR_LEN    12
#define EK_MAX_BODY   512
#define EK_TAG_LEN    16
#define EK_CRC_LEN    4
#define EK_FLAG_ENC   0x01
#define EK_FLAG_CRC   0x02

typedef struct {
    uint32_t magic;   /* EK_MAGIC */
    uint16_t len;     /* body 字节数,不含头/tag/CRC,1..512 */
    uint8_t  flags;   /* bit0: GCM;bit1: CRC32;合法组合见下表 */
    uint8_t  rsvd;    /* 必须为 0 */
    uint32_t seq;     /* TCP:握手0,加密后每方向独立递增;串口:请求id/应答回填 */
} ek_hdr_t;           /* 12B */

/* 帧 = header[12] | body[len] | trailer(由传输阶段固定,不能协商降级) */
```

头部布局只有一份定义,客户端和固件的 TCP/串口共用编解码器。C struct 仅描述字段,不能依赖编译器 padding、主机端序或直接强转读写;实际逐字段编码,并验证 sizeof/offsetof 与下表一致。

| 偏移 | 长度 | 字段 | 线格式 |
|---|---|---|---|
| 0 | 4 | magic | 固定字节 45 4B 56 4D,即 EKVM |
| 4 | 2 | len | uint16 LE,仅 body 长度 |
| 6 | 1 | flags | 由下表固定 |
| 7 | 1 | rsvd | 必须为 0 |
| 8 | 4 | seq | uint32 LE,语义按传输阶段区分 |

| 传输阶段 | flags | body | trailer | 整帧最大长度 |
|---|---|---|---|---|
| TCP 握手 | 0x00 | 明文 Msg | 无 | 524 B |
| TCP 已认证 | 0x01 | GCM 加密的 Msg | 16 B GCM tag | 540 B |
| 串口配网 | 0x02 | 明文 SerialPacket | 4 B CRC32 LE | 528 B |

0x03、未知 flag 位及不符合当前传输阶段的 flags 均拒绝。串口不接受 flags=0 的 TCP 握手帧,TCP 不接受 CRC 帧;不能由来包自行切换校验模式。GCM 的 AAD 和串口 CRC 均覆盖原始完整头部。TCP 不额外叠加 CRC。

### 5.2 接收规则

#### 5.2.1 TCP

1. `recv_exact(12)` 读满头(处理拆包)。
2. `magic` 不符、`len == 0 || len > 512`、`rsvd != 0`、`flags` 与当前阶段不符(握手前必须明文,握手后必须加密)→ **直接断开**。
3. `recv_exact(len + (enc ? 16 : 0))`。
4. 加密帧:要求 `seq == expected_seq`,GCM 解密失败 → 断开。
5. `pb_decode` → 按 `oneof` 分发 → `expected_seq++`。

头部逐字段读写,不得直接传输 C struct 的内存表示。握手 flags 必须恰为 0、seq 恰为 0;认证后 flags 必须恰为 1,未知 flag 位直接拒绝。单帧从首字节起的读取期限为 1 s,另受握手总期限及 3 s 会话期限约束。

`Msg.body` 必须存在,方向和阶段必须合法:设备先发 Challenge,客户端只回 Auth,设备只回 AuthOk/AuthFail;客户端验证 AuthOk 后才发送加密消息。认证后 C→D 允许输入、release_all、heartbeat、cfg_get、cfg_set、reboot;D→C 允许 heartbeat、status、cfg_data、cfg_result、session_end(reason=1)、released(id)。解码失败、空 body、未知消息类型、非法方向或越界值均断开并释放;已知消息内的未知字段可忽略。nonce、HMAC、MAC 必须验证精确长度,缺失字段不能用全零值代替有效数据。协议版本不等于 2 时拒绝,不自动降级。

**不做 magic 重同步**:TCP 可靠有序,错位只可能是 bug 或恶意流量,一律断开重连。Magic 的作用是快速识别非本协议流量并便于抓包调试。握手前 magic 错误的连接,直接关闭、不回数据。粘包靠"一次只读一个头加一个 body"处理,客户端读端需套 `bufio.Reader`。

#### 5.2.2 串口

1. 用固定容量环形缓冲区扫描 Magic 的四字节序列,无匹配时只保留可能组成下一 Magic 的末尾最多 3 字节。打开端口不假设正好位于帧头。
2. 找到候选后读满 12 字节,检查 len 为 1..512、flags=0x02、rsvd=0、seq 非零;不合格则从候选起点后一个字节继续扫描。
3. 合格头部等待 `12 + len + 4` 字节,等待期间不因 payload 内的 Magic 提前截帧。候选总读取期限为 1 s,从候选首字节到达时起算,不因后续字节延长。
4. 校验 CRC(header || body)。CRC 错误或候选超时,从原候选起点后一个字节重新扫描**已经缓存的数据**,不能按错误 length 丢掉整段,以免吞掉其中的有效后续帧。一个坏 length 可能造成最多一个候选期限的恢复延迟。
5. CRC 正确才解码 SerialPacket。Protobuf 损坏/空 body 时消费这一完整帧并静默丢弃;合法消息再检查版本、方向、seq 关联和字段,按 4.1 节执行或返回错误。任何验证失败都不能触发配置副作用。

串口不套用 TCP 的 expected_seq++ 或认证状态机。接收缓冲区至少 528 字节,按容量逐步从驱动读取并及时消费,禁止无界积累;解析器仅移动游标和保存必要缓存,无需转义/还原 payload。公共头部编解码、body 长度上限和配置服务复用,两种传输的错误恢复策略分别实现。

### 5.3 加密与握手

```
1. D→C  Challenge{ nonce_d[16], proto_ver, fw_ver, mac, paired, name }   明文, seq=0
2. C→D  Auth{ nonce_c[16], hmac[32], owner[16], takeover }                                     明文, seq=0
        hmac  = HMAC-SHA256(PSK, "EK-AUTH-C" | auth_context | nonce_c)
3. D→C  AuthOk{ hmac[32] }                                                明文, seq=0
        hmac2 = HMAC-SHA256(PSK, "EK-AUTH-D" | auth_context | nonce_c)
4. 双方派生:key = HMAC-SHA256(PSK, "EK-KEY" | auth_context | nonce_c)[0:16]
5. 此后所有帧 flags.bit0=1,使用 AES-128-GCM,每方向 seq 从 0 开始
```

`auth_context = SHA256("EK-AUTH-V2" || transcript_hash[32] || owner[16] || takeover[1])`，takeover 编码为 0 或 1。owner 为客户端进程实例随机 16B，自动重连保持不变；更换 owner 的抢占须显式授权。三个认证标签及会话密钥统一使用 auth_context，任何 owner/意图篡改均认证失败。

`transcript_hash = SHA256(Challenge 帧的原始 12 字节头 || 原始 body)`;双方保留原始字节,禁止重新序列化后计算。字符串标签为 ASCII,不含 NUL;hash 为 32 字节,nonce_c 为 16 字节,拼接无歧义。这样版本、设备元数据和 nonce_d 均被认证绑定。元数据在 AuthOk 校验成功前只作“不可信”展示。PSK 始终为随机 16 字节。本握手不提供前向保密,PSK 泄露后历史抓包也可能被解密。

| 项 | 规定 |
|---|---|
| AAD | 整个 12 字节头(含 magic) |
| Nonce(12B) | `[dir, 0, 0, 0, seq(4B LE), 0, 0, 0, 0]`;`dir`:0 = C→D,1 = D→C |
| 防重放 | `seq` 必须严格等于期望值,否则断开 |
| 密钥生命周期 | 每次连接重新派生;`seq` 到 `0xFFFFFF00` 时主动断开重连 |
| 比较 HMAC | 必须**常量时间**比较 |
| 随机数 | 固件 `esp_fill_random`(WiFi 开启后为真随机),客户端 `crypto/rand` |
| 双向认证 | 客户端也校验设备的 `hmac2`,防止假设备 |

> **安全边界**:首次配网的 PSK 经串口明文写入(本机 USB 线上,可接受)。握手包中的设备名、MAC、固件版本为明文,不涉及密钥。

### 5.4 Schema 与生成约束

共享定义只保留在 [ek.proto](../sources/protocol/ek.proto) 和 [ek.options](../sources/protocol/ek.options)，禁止复制一份 schema 到设计文档。版本为 2，生成目标及固定工具链见 CMake。nonce/HMAC/MAC/owner 使用有长度的字节数组，逐字段检查精确长度，拒绝缺失字段。

新 schema 使用 `optional string password` / `optional bytes psk`：缺省表示保留；显式空 password 表示开放网络；显式 PSK 必须恰为 16B，显式空 PSK 拒绝。首次配置必须提供这两个字段（开放网络提供存在的空 password），不得从脱敏快照凭空补零。

非敏感字段继续完整快照替换。服务在锁内把缺省敏感字段与活动配置合并，再统一校验和原子保存。CFG_DATA/info 不返回这两个 optional 字段，不用“存在的空值”伪装脱敏。修改名称不再要求重新填写密码。pending 比对要忽略敏感字段的存在性，并继续保留改 PSK 前后的候选凭据。

新消息使用 `Heartbeat { uint32 id; bool reply; }`。每方向独立生成非零 id；请求 `reply=false`，对端只回一次相同 id、`reply=true`，响应不再回显。每方向最多一个在途探测；未知/重复/过期响应不形成 RTT 样本，连接更换后丢弃旧探测。

RTT 从 writer 开始发送探测到收到并认证匹配响应，用单调时钟计量，记录样本数、窗口和 p50/p95/p99。它包括网络/两端调度，**不是输入→受控机事件延迟，也不是 USB 完成确认**。第 11.1 节端到端基线仍须目标机观测、可对齐时钟或外部采集，独立报告。

### 5.6 Protobuf 使用要点

ReleaseAll/Released 的 id 非零，ReleaseAll.id 从 1 严格递增，禁止回绕。设备可把连续请求合并到更强的同一 USB 屏障，但必须分别确认所有 id；控制等待者上限 128。只有主机在用的接口的全释放报告均完成后才能发送 Released(见 6.1 节)。唯一例外是 USB 主机睡眠:挂起本身已清空逻辑状态并置 pending-release,恢复或重新枚举后先发中性报告再输入,因此挂起期间(及唤醒时总线复位后 20 s 内)ReleaseAll 立即确认,使切换在目标机睡眠时也能完成,随后用户的键鼠输入触发远程唤醒。客户端从调用起最多等待 500 ms;设备状态 `usb_suspended=true` 时放宽到 20 s。超时关闭会话并唤醒全部等待者;未知或重复 Released 视为非法消息。500 ms 是待实测确认的初始安全边界。USB 真正卸载(无睡眠宽限)时不得确认成功,重挂后先完成 pending-release。

- 固件用 **nanopb**,禁用 callback 字段与动态分配;`Msg` 与收发缓冲区按任务/会话独占静态分配,不放任务栈;禁止跨任务共享可变解码对象。
- proto3 零值不编码:键盘全释放编码为空 `KbState`,解码得到全 0,语义正确。
- 键位图用四个 `fixed64`,覆盖 0..255;0..3 保留位及 0xE0..0xE7 对应位必须为零。mods 只允许低 8 位,鼠标 buttons 只允许低 5 位。Consumer Usage 使用实现公布的支持列表,不支持项不转发。
- 有符号数一律 `sint32`(ZigZag),负的 `dx/dy` 只占 1~2 字节。
- 凭据存在性按 5.4 节处理；存在的 PSK 必须 16B，缺省保留；首次必须同时提供 password 和 PSK。
- 鼠标四轴内部用 `int64` 累加并检查加法溢出;每帧每轴限制在 ±1000000,越界拒绝。发送协议时分段为合法 sint32 值,发送 HID 时按描述符范围分段,只扣除已成功提交的量。
- `Empty` 在 `oneof` 中保留存在性，用于 cfg_get/reboot；Heartbeat、ReleaseAll 和 Released 必须具有非零 id。

## 6. 固件设计

任务划分:

| 任务 | 职责 |
|---|---|
| `wifi_task` | STA 连接、静态 IP、断线重连 |
| `serial` | 共享帧头/CRC/SerialPacket 解析、配置/重启/恢复出厂,复用配置服务;日志钩子输出 log 帧 |
| `accept` / `session` | Accept、握手、GCM、nanopb 解码、入队;每连接一个 session 任务 |
| `watchdog` | 25 ms 轮询认证有效帧时间、WiFi 状态和 HID 故障 |
| `hid` | 维护键盘/鼠标状态,合并鼠标 delta,按 1 ms 节拍发送 HID 报告;防休眠模拟移动 |
| NVS | WiFi 凭据、IP、端口、名称、PSK、USB VID/PID、six_kro、keep_awake |

行为要点:

- **安全释放**:TCP 断开、心跳超时、队列溢出、USB 卸载、收到 `release_all` 时执行第 6.1 节释放屏障,覆盖键盘、鼠标和 Consumer。
- 认证前收到加密类消息,或加密后收到握手类消息,一律断开。
- 支持 CFG_SET 非敏感字段完整快照替换，缺省凭据保留，按第 6.2 节提交后重启。
- 可选:RGB LED 指示状态,BOOT 键恢复出厂。

### 6.1 USB 交付与释放屏障

- 状态转换使用容量为 128 的有界 FIFO,不能只保存最后状态。按下与抬起即使在一个 USB 轮询周期内到达也必须分别提交。只在同一按钮状态的连续段中合并位移,禁止跨按钮边界合并。队列项保存值副本,不得引用可被下次解码覆盖的 Msg。
- HID endpoint ready 时提交队头,提交成功后等待完成回调再推进;失败不扣除位移。1 ms 为轮询目标,不是网络或端到端延迟保证。跨接口按 FIFO 顺序完成提交,明确区分已入队、已提交及 USB 传输完成。
- 释放屏障通过独立高优先级控制状态触发,不得排在满队列尾部:废弃旧输入和位移、清零逻辑状态,等待在途报告完成后优先提交各接口全释放。屏障完成前不提交新输入;断线时同时使旧会话队列失效。
- 队满或最老输入等待超过 100 ms 时关闭会话并触发屏障。USB 不可用时无法保证实际送达释放,记录 pending-release;重新枚举或恢复后首先发送全释放。同一代的输入可排在这一传输屏障之后,但屏障完成前不提交。
- 接口使用状态:主机读取报告描述符或发送 SET_IDLE/SET_PROTOCOL/GET_REPORT/SET_REPORT,或接口完成过传输,才算"在用"。主机未使用的接口(例如 BIOS 只驱动 Boot 键盘)不参与释放屏障,发往它的输入直接丢弃。
- 提交后 100 ms 未完成视为该端点停止轮询:放弃该传输、标记 stalled 并触发故障屏障,屏障跳过 stalled 接口,不能让一个接口卡死全部接口。被放弃的报告若之后迟到完成,主机可能持有过期报告,立即补发该接口的当前逻辑状态。
- SET_IDLE 重复报告单独标记,完成回调只结束本次传输,不推进 FIFO,也不重复相对位移。
- 独立看门狗检查认证有效帧时间,到 3 s 时触发屏障并关闭会话。网络任务不能因队列或 USB 等待阻止看门狗运行。

### 6.2 配置事务

- CFG_SET 完整替换非敏感字段。optional password/psk 缺省时在同一事务锁内保留活动值；显式空 password 是开放网络，显式空 PSK 拒绝。读取时两个字段均不出现。首次须同时提供凭据。
- 校验名称 UTF-8 最多 31 字节、SSID 1..32 字节、密码为空或 8..63 字节口令或 64 位十六进制值、PSK 存在时 16 字节、端口 1..65535、VID/PID 1..65535、连续掩码及主机/网关地址合法性。所有字段先校验,失败不改变活动或持久配置。USB VID/PID 只能采用有合法使用依据的值。
- 同时只允许一个配置事务,串口与 TCP 共用互斥锁。候选配置以带版本和校验的单个 NVS blob 保存,采用双槽及递增版本号;完整写入并 commit 后才作为有效版本,启动选择最新完整有效槽。断电不能产生字段混合。
- 提交成功后先触发输入释放,发送 CFG_RESULT(code=0),等待发送完成或最多 200 ms 后重启;失败返回错误且不重启。TCP 上 `send()` 只代表进入 lwIP 缓冲,因此写出后 `shutdown(SHUT_WR)`,等待对端读完结果后关闭连接或 200 ms 期限到达再重启;客户端在连接关闭时优先采用已到达的 CFG_RESULT。客户端每会话最多一个 CFG_SET 在途,不自动重发,不允许切换后仍给旧连接写配置。
- 客户端发送前原子落盘旧配置与 pending 新配置;应答丢失或连接断开不等于失败。尝试新 IP/PSK 并认证,读取非敏感配置核对后标为 ready;失败时保留两份候选供恢复,不无界循环改配置。CFG_RESULT 成功只代表持久化成功,不代表新 WiFi 一定可达;错误网络参数通过串口恢复。

## 7. 客户端设计(Go)

源码目录以 [源码目录与 WSL 开发约定](source-layout.md) 为准:Go 客户端采用单 module,程序入口放 sources/host/cmd,实现放 sources/host/internal;设备源码放 sources/device,共享协议源放 sources/protocol。

### 7.1 输入捕获与拦截

| 平台 | 捕获 | 拦截 |
|---|---|---|
| Windows | Raw Input + LL 钩子(线程 `LockOSThread` 并跑消息循环) | 钩子回调返回 1 吞掉事件;远程模式下 `ClipCursor` 锁定本机光标。钩子回调中**只入队,不做网络 IO** |
| Linux | `/dev/input/event*`，LOCAL 被动读取 | 仅 REMOTE 使用 EVIOCGRAB，进入时全释放边界抓取；失败/退出立即解除 |

限制:Windows 涉及完整性级别和安全桌面的行为必须分场景验证,远程控制管理权限窗口时按需提权;不承诺拦截 `Ctrl+Alt+Del`、`Win+L`。Linux 正常转发只需要选定 evdev 设备的权限；uinput 只在另行启用的修复/实验场景申请，不作为普通输入通路。

- Windows 钩子必须同步决定吞放,不能先交给异步 dispatcher 再决定。钩子线程维护最小路由/热键状态,按事件序号与 dispatcher 衔接,回调只非阻塞入队。键盘和按钮以钩子为唯一来源,Raw Input 只取相对位移及滚轮,避免双计数;验证低级钩子吞键后 Raw Input 的实际可用性。回注带自定义标记并排除,避免递归;退出、会话锁定或切换桌面时撤销 ClipCursor 并回 LOCAL。
- Linux 按 SYN_REPORT 处理批次,EV_KEY value=2 不重复改变状态;监听热插拔、排除本程序 uinput 设备、处理 SYN_DROPPED 并通过 EVIOCG* 重建状态。回注设备声明所需能力及 SYN_REPORT。多个物理设备同按一个键时使用每设备状态与引用计数,拔出设备释放其贡献。
- Linux LOCAL 不抓取、不回注。正常进入远程须物理全释放，在批次边界抓取并重读状态；任一失败撤销所有抓取。故障立即解除抓取。LOCAL 已到桌面的输入不能用另一个 uinput 设备撤销；LOCAL 可能看到整个热键组合。
- 正常回 LOCAL 先确认远程释放，优先等物理键/按钮全释放的批次边界再解除抓取，dispatcher 仍处理输入。故障、取消或退出立即解除，不为等待物理释放延误本机恢复。恢复时可能出现物理保持键的后续原生事件，抑制集合不承诺过滤 Linux LOCAL 的桌面输入。

### 7.2 发送策略

- 位移和滚轮仅在相同按钮状态的相邻段内累加,约 **1 ms** 尝试取走一次;按钮边界封存此前移动段。合并只能减少未写入 TCP 的数据,不能撤回内核缓冲区的数据,不承诺完全消除“鼠标回放”。
- **键盘和鼠标按键不合并**,必须按序发送;键盘状态每次变化立即发送;鼠标按键变化立即 flush。
- 热路径复用缓冲区与 `Msg` 对象,用 `MarshalAppend` 序列化后原地 `Seal`,避免 GC 压力。
- 原地加密使用 `gcm.Seal(pt[:0], nonce[:], pt, hdr)`;完全分离的 dst 也合法,禁止不完全重叠。
- 读端套 `bufio.NewReader`;写端头、密文、tag 在同一缓冲区一次 `Write`。
- 每会话一个 reader、一个 writer 及生命周期管理器;所有输入/心跳/配置只由 writer 编码、分配 seq 并写入。处理短写;部分写入后失败立即断开,不得在同一连接复用 seq 重试。
- 应用发送队列最多 128 条,最老输入队龄上限 100 ms,每次写入总期限 100 ms。队满、超龄或写超时关闭会话并回 LOCAL,不静默丢失状态转换。dispatcher 和钩子不得等待网络写入。阈值是初始实施值,经实测可调整并记录依据。

## 8. 热键与统一切换状态机

默认热键为左 Ctrl、左 Alt 和 W 的 HID Usage 集合 `{0xE0, 0xE2, 0x1A}`,支持 Web UI 录入 2..8 个不同键的其他组合。物理按键状态与已转发状态分开维护;不得因切换清空物理状态。

热键触发来源(均进入同一 dispatcher 和第 8.3 节切换流程):

| 来源 | 平台 | 条件 | 方向 |
|---|---|---|---|
| 物理状态组合 | Windows LL 钩子 / Linux evdev | 当前配置的 Usage 集合,按 8.1 节语义 | 正向 |
| Raw Input 识别 | Windows,仅默认热键 | 任一侧 Ctrl + 任一侧 Alt + W 组合形成的边沿 | 正向 |
| RegisterHotKey | Windows,仅默认热键 | 系统 `MOD_CONTROL|MOD_ALT|MOD_NOREPEAT` + W,钩子收不到事件(如焦点在提权窗口)时兜底 | 正向 |
| 鼠标组合 | Windows | 按住任一侧 Ctrl 时按下 XButton1 / XButton2;侧键本身被吞掉,不转发 | XButton1 正向,XButton2 反向 |

后三个来源 200 ms 内只触发一次,与钩子路径共同去重;录入热键期间都不触发。热键改为非默认组合时注销 RegisterHotKey 并停用 Raw Input 识别。部分鼠标驱动以 `VK_BROWSER_BACK/FORWARD` 报告侧键,钩子把这两个虚拟键视为 XButton1/XButton2。

### 8.1 热键语义

- 组合最后一个键按下,且没有组合外的键或鼠标按钮按住时触发一次。自动重复不触发;所有组合键均释放后才重新武装。录入模式不执行切换。
- 明确取舍:不延迟普通 Ctrl 操作,因此首个 Ctrl 可短暂被当前目标看到。只吞掉完成组合的按下以及后续组合键事件,并释放旧目标已收到的 Ctrl。不能承诺整个热键零泄漏,具有单 Ctrl 动作的软件可能观察到它。
- 触发后等待组合键全部释放,期间不转发键盘、鼠标和 Consumer 输入。期间新按下的键/按钮加入抑制集合,直到各自物理释放才解除;位移直接丢弃。无在线目标时仍完成本次组合的吞键及释放,保持 LOCAL。

### 8.2 目标与轮换

目标为 `LOCAL, DEV0, DEV1, ...`,按设备列表顺序循环,仅包含已启用、已认证且 USB 已挂载的设备;USB 主机睡眠中(状态 `usb_suspended`)仍算已挂载。重排按稳定设备 id 保持目标。Web UI 可直接选择目标,与热键共用以下流程;反向轮换只由 Windows 的 Ctrl+XButton2 提供,不提供直接跳转热键。

活动设备断线、USB 卸载、删除或禁用时立即回 LOCAL;自动重连成功后仍保持 LOCAL。非活动设备只收发心跳、状态和配置,不接收输入。

### 8.3 统一切换流程

唯一 dispatcher 串行处理输入和切换,拥有目标、物理状态、已转发状态、鼠标累加量及 generation。步骤如下:

1. 增加 generation,进入切换抑制状态;将当前按住的键/按钮加入抑制集合,保留物理状态,清除待发送位移。切换期间新按下的键/按钮也加入集合,不把旧拖拽或修饰键继承到新目标。
2. 旧目标为 Windows 本机时，通过 SendInput 补发其已收到的键、按钮释放；Linux LOCAL 则等待物理全释放边界抓取，不用 uinput 撤销原生桌面输入。旧目标为远程时,writer 丢弃未开始编码的旧代输入并优先写 release_all;在途 Write 不能撤回,必须在其 100 ms 期限内完成,随后发送释放,失败则关闭连接。dispatcher 异步接收结果,仍能处理输入或取消。
3. 正常切换等待对应 Released{id}（三个接口的全释放均已完成），失败时关闭旧连接后继续。仅写出不等于 USB 已释放；网络故障下旧设备可能保持按键直到 3 s 看门狗到期，界面保留“释放未确认”的警告。故障回退 LOCAL 不等待旧网络写入，立即撤销 ClipCursor 和远程吞键模式。
4. 新远程目标须再次检查就绪状态，先由其 writer 写 ReleaseAll{id} 并收到匹配的 Released{id}，再开放当前代输入，失败回 LOCAL。远程到远程期间保持本机输入拦截，不回注本机。
5. 切换完成后允许新的普通输入,抑制集合中的输入仍等待各自释放;热键切换还须等组合键全部释放。任何切换来源均执行同样的状态清理。

每条排队输入带本地 generation 和会话实例标识,writer 编码前再次验证,旧代数据或旧连接回调不得污染新会话。每设备保留独立 Conn、GCM、seq、缓冲区和队列。切换请求串行执行,进行中只保留最后一个用户目标请求;LOCAL/故障回退优先取消远程切换。

用户在切换进行中选择 LOCAL 是取消,不是故障,不关闭任何会话:旧远程目标仍在释放时等它的 Released 后回 LOCAL;正在等待新目标释放或 Linux 抓取边界时,放弃该等待(generation 递增,迟到结果被忽略)并立即回 LOCAL。只有释放失败/超时、设备断线、USB 卸载、输入队列溢出等故障才关闭连接。

每设备独立生命周期管理器负责连接、握手、并行收发及重连,退避为 1 s、2 s、5 s 并加入随机抖动。输入队列溢出视为状态失步,关闭活动连接并恢复本机输入;Linux 先解除抓取,同步物理状态后才允许恢复抓取转发。

## 9. Web UI 与安全

- 仅绑定 `127.0.0.1:62626`，静态资源 go:embed。

改为 SSE 状态推送 + POST 操作，不引入 WebSocket 依赖。SSE 只发脱敏状态，限制事件长度/客户端数，并处理断线恢复；POST 仍做 Host、Origin 和 CSRF 校验。

访问控制以登录密码为准。`POST /api/login` 校验密码后签发随机 HttpOnly、SameSite=Strict、Path=/ 的会话 cookie(8 h)及 CSRF。初始密码为 `admin`,Web UI 可修改(8..128 字节)。密码在客户端配置文件中以 `web_password_md5 = md5("espkvm_" + 密码)` 的小写十六进制保存;旧版 PBKDF2 记录在下次成功登录时转换。

每个会话记录签发时的密码摘要,每次请求及 SSE 每次推送都与配置中的当前摘要比较;任何进程(本程序、另一实例或手工修改配置)改了密码,旧会话立即失效,必须用新密码重新登录。可选的 `-bootstrap-file` 一次性启动凭据(URL fragment,2 分钟过期)仅在密码仍为默认值时可用,改密后被拒绝。cookie 不取代写请求的精确 Origin/CSRF 防护。

默认密码未修改前,本机任何进程都能登录,首次使用应立即改密。未加盐 MD5 只防止明文存放,不能抵抗拿到配置文件后的离线猜测;配置文件仍须按下文权限保护,登录接口目前不限速。该模型不是同一 OS 用户下恶意进程的隔离边界,多用户部署仍须每用户私有凭据,必要时使用 OS 认证 IPC。

- 精确校验 Host 与写请求 Origin，cookie 及 CSRF 均须验证；拒绝 null/缺省 Origin 的写请求。GET 无副作用；敏感读取和 SSE 须 cookie 与 CSRF 请求头；CSRF 只从登录(或启动交换)获取并放在同源 sessionStorage，不能凭 cookie 重新领取。SSE 用 fetch 流读取，不把凭据放入 query；不启用宽泛 CORS。
- 功能：设备管理与排序、统一切换、热键录入、串口配网及 pending 查询/丢弃、设备配置、脱敏状态与 RTT 指标。
- 本地凭据文件 Linux 0600/目录0700；Windows 当前用户与系统 ACL；原子替换并持有稳定 sidecar 跨进程锁。pending 同样保护，日志不输出凭据。

## 10. 开发环境与流程

具体目录、生成代码、构建产物及 Windows/WSL 衔接见 [源码目录与 WSL 开发约定](source-layout.md)。

```
WSL2 (Ubuntu 22.04/24.04)
 ├─ ESP-IDF v6.0.3 + esp_tinyusb 3.0.0   idf.py build → build/*.bin、flash_args
 ├─ CMake/Ninja 自动调用 nanopb + protoc 生成 ek.pb.c/h
 └─ Go 工具链(CMake/Ninja 自动生成 ek.pb.go 后构建 host)

Windows
 ├─ 烧录:flash.ps1 读取打包 manifest,调用 Windows esptool
 │        (打包后通过 WSL UNC 路径读取自包含烧录包)
 └─ 调试日志:espkvm ctl -serial COMx serial-log 解析配网 UART 上的 log 帧;不运行文本 monitor
```

- 工程放 WSL 内部文件系统,编辑用 VS Code + WSL 扩展 + Espressif IDF 扩展。
- 提供 WSL 构建/打包入口与 Windows flash.ps1;烧录包使用相对路径,不直接把含 Linux 路径的 flash_args 传给 Windows。
- 暂不使用 `usbipd-win`(ESP32-S3 烧录时 USB 重枚举,重新绑定麻烦)。
- CI:GitHub Actions,固件用 `espressif/esp-idf-ci-action`,客户端交叉编译 Go;通过 CMake/Ninja 依赖 sources/protocol/ek.proto 自动生成并编译两端代码,验证协议一致。
- 锁定 ESP-IDF、esp_tinyusb、nanopb、protoc、Go 和第三方库版本,不用无上限的 `5.2+` 作为实际依赖。生成 Go 代码时提供稳定 go_package 或生成器 M 参数。CI 额外执行跨语言字节向量测试,仅两端编译成功不能证明互通。含 CGO 的平台依赖使用目标平台 runner 构建。
- 实机联调在本地进行,日志与报错贴回对话定位。

## 11. 里程碑

顺序改为：USB 三接口测试固件 → 最小 Windows 钩子/Raw Input 实验及 Linux 模式切换实验 → 实机敲字/恢复本机输入 → 冻结新协议 → 会话与完整客户端。普通编译不代替 Windows/Linux/BIOS 实机验收。

M1 使用独立 `experiments/usb-hid` 工程，不开放 UART 输入注入，不依赖 WiFi 或网络协议。构建目标 `hid-selftest` / `rebuild-hid-selftest`，烧录包目标 `package-hid-selftest`。报告编码器放在 `sources/device/components/hid_reports` 供复用；实验中的单生产者同步提交不是正式固件的 FIFO、任务间并发或会话释放屏障实现。

持久化键盘兼容模式。强制模式同时使用标准 Boot Keyboard 报告描述符和 8B 报告，即使主机不发 SET_PROTOCOL 也保持 6KRO；不能只把 NKRO 描述符对应的报告缩成 8B。普通键超过六个时报告 ErrorRollOver。模式修改后重新枚举/重启。

M1 的模式保存在独立 `hid_test` NVS 命名空间，长按测试按钮切换并重启。正式 Config 使用 six_kro 字段；实验的 hid_test 命名空间不作为正式配置。



| 阶段 | 内容 | 验收 |
|---|---|---|
| M0 | 输入平台实验:Windows 同步吞键/Raw Input/回注;Linux 抓取/回注/热插拔 | 两个平台都可恢复本机输入,确认权限及依赖边界 |
| M1 | 固件:USB 复合 HID + 测试固件注入 | 受控机识别键鼠,测试固件敲字、移鼠标(发布固件不开放串口输入注入) |
| M2 | 固件:WiFi 静态 IP + TCP + 握手 + GCM + nanopb | 命令行测试客户端能认证并发送输入 |
| M3 | 客户端:协议层、Linux/Windows 捕获、热键切换、键码映射 | 完整可用的键鼠共享 |
| M4 | Web UI:设备管理、热键录入、串口配网、配置读写 | 可用图形界面完成全部操作 |
| M5 | 故障注入、状态显示、日志、安装脚本;OTA 延后单独设计 | 通过下述发布验收 |

### 11.1 必须通过的验收

| 场景 | 预期结果 |
|---|---|
| 跨语言协议向量 | 固定 Challenge 原始字节、PSK、nonce_c 时,C/Go 得到相同 HMAC、会话密钥、AAD、密文和 tag;覆盖负位移、全释放、最大配置 |
| 串口帧与恢复 | C/Go 的共享头部和 CRC 字节向量一致;覆盖 payload 内嵌 Magic、伪帧头、错误 length/flags、最大包、拆包/粘包、错误 CRC、截断、超长、超时和启动日志噪声;坏候选内部的后续完整帧可恢复,坏帧不修改配置 |
| 串口事务 | request_id/应答类型匹配,仅允许配网消息;首次空 PSK 拒绝,保存应答丢失不盲重发;CLI 和 Web UI 复用同一协议 |
| 错误帧与慢连接 | 拆包/粘包正确;错误长度、flags、方向、版本、字段长度、tag、seq、空 body 均拒绝;零碎字节不能延长超时 |
| 快速点按 | 同一 USB 就绪间隔内的键/按钮按下再抬起,受控机均观察到两次转换;Consumer 按下/释放正常 |
| 位移与拖拽 | 大位移分段后总和不变,移动→按下→移动→抬起顺序不变;正负滚轮均正常 |
| 拥塞和队满 | 触发 100 ms 队龄/写超时或队满即断开回 LOCAL;钩子/dispatcher 不阻塞,旧代事件不进入新目标 |
| 断网及恶意保活 | 按住键、鼠标、Consumer 时断网或仅发送零碎无效帧,固件在最后有效帧后 3 s 触发释放;USB 可用时按完成回调确认释放 |
| 切换 | LOCAL→DEV、DEV→DEV、DEV→LOCAL、Web UI 按住 Shift/拖拽切换均释放旧状态;组合一次只切一次;重连不自动切回远程 |
| 平台恢复 | Windows 注入事件不递归,锁屏/桌面切换后恢复本机;Linux SYN_DROPPED、设备拔插、多键盘同键、进程退出后无持续卡键 |
| 配置事务 | 非法配置不落盘;保存中断电保留完整旧或新配置;应答丢失及改 IP/PSK 可通过 pending 记录恢复;错误 WiFi 可串口救援 |
| USB 兼容性 | 报告模式/Boot 模式切换、6 键溢出、LED、多媒体键、卸载重枚举,以及目标 Windows/Linux/BIOS 实机通过 |
| 本地 UI 安全 | 错误 Host/Origin、缺失 cookie/CSRF、跨源 POST/SSE、敏感未授权读取均拒绝;日志无 PSK/密码 |

性能报告记录输入到受控机事件的 p50/p95/p99、测试时长、丢转换数、队列峰值及 WiFi 条件;先建立实测基线再冻结延迟指标,不能把 1 ms 发送节拍当作端到端延迟。

## 12. 已知风险与注意事项

1. **WiFi 抖动**:偶发 20~100 ms 延迟尖峰;靠鼠标合并发送与心跳超时释放缓解。ESP32 仅支持 2.4 GHz,尽量靠近路由器。
2. **TCP 队头阻塞**:应用层合并不能消除,以有界队列、写期限和超龄断开限制影响。
3. **键盘内容隐私**:已用 GCM 加密;PSK 泄露即失去保护,需保护好客户端配置文件。
4. **BIOS/引导阶段**:以 Boot Keyboard 为兼容性基线,在目标 BIOS/UEFI 验证;不承诺所有固件支持复合设备或鼠标。
5. **杀毒/反作弊**:全局钩子可能被部分安全软件和游戏反作弊误判。
6. **Windows 权限**:提权窗口、`Ctrl+Alt+Del`、`Win+L` 无法拦截。
7. **`seq` 溢出**:`seq` 到 `0xFFFFFF00` 前必须断开重连换密钥,否则 nonce 复用。
8. **设备唯一 PSK**:每台设备应使用不同随机 PSK,不要复用。
9. **多设备切换时的卡键**:遵守第 8 节统一切换屏障;首个 Ctrl 可见的限制见 8.1。
10. **活动设备断线**:一律回退 LOCAL,不要停留在"无人接收"的远程状态,否则本机输入会被独占却无处可去。

## 13. 实施追踪

本文及 schema 是唯一行为基线。实现进度见 [实施状态](implementation-status.md)，实机和性能验收见 [验收记录](validation-2026-11-02.md)。自动测试、编译、台架和目标机实测分别记录，未采集的数据不视为通过。
