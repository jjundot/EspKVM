# ESP32 键鼠共享器:设计决议汇总 (v10)

> **2026-11-01 修订**：[硬件优先与设计修订](design-review-2026-11-01.md) 覆盖本文的 Linux LOCAL 常驻抓取、单握手槽拒绝接管、敏感字段完整替换、Empty 心跳、UI token 分发及 SDK 基线。以下线格式仍是当前可构建的 v1 草案；M1/最小 M0 后集中更新 schema 和协议版本，尚未冻结为发布协议。

> 本文为实施基线,替代 v9。v10 将串口与 TCP 统一为 Magic + length 的同一 12 字节帧头,串口使用 CRC32,移除 COBS;协议版本为 `proto_ver = 1`,实现前按本版生成协议文件。性能与 BIOS 兼容性仍须实机验证,不作为已验证承诺。

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
| 切换热键 | **左 Ctrl + 右 Ctrl 同时按下**,Web UI 可改 |
| 多设备 | 客户端**同时连接多台 ESP32**,但同一时刻只控制一台;同一热键按顺序**循环切换**:本机 → DEV0 → DEV1 → … → 本机 |
| 多设备协议/固件 | 使用同一套单会话固件,不增加跨设备协议。每台设备独立 IP、独立 PSK、独立会话;切换逻辑在客户端 |
| 鼠标 | 仅**相对鼠标**,走 TCP;客户端累加 delta,不排队 |
| 键盘 | 发送**完整状态**(NKRO 位图),不发按键事件 |
| 不做 | 边缘触发、绝对鼠标、剪贴板共享、SoftAP、UDP、端口扫描、TCP 协议重同步 |

## 3. 硬件

- 板子使用 ESP32-S3 开发板(如 ESP32-S3-DevKitC-1)。
- **原生 USB 口** → 受控电脑(同时供电、提供 HID)。
- **UART 口** → 非受控电脑(烧录、首次配网;应用日志不混入配网协议)。
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

串口 header.seq 用作 request_id,非零递增,应答原样回填;它不使用 TCP 的每方向严格递增校验,不作为加密 nonce。SerialPacket 不重复存 request_id。客户端最多一个请求在途,只接受当前 seq 的预期应答或错误 ack;打开端口时清空接收缓存并随机选择非零起始 seq,之后递增,到 UINT32_MAX 后关闭并重新打开。request_id 只关联应答,不提供持久去重或 exactly-once 保证。proto_ver 必须为 1。允许消息:

| 请求 | 应答 | 行为 |
|---|---|---|
| info_get | info | 返回固件版本、MAC、paired、脱敏 Config,不返回 PSK/密码 |
| cfg_set | cfg_result | 复用完整 Config 校验及原子保存;成功后按 6.2 节应答并重启 |
| reboot | ack | 先应答后重启 |
| factory_reset | ack | 清除配置,持久化成功后应答并重启;失败返回非零 code |

合法信封中的不支持消息/版本返回 ack 非零 code,不执行配置。串口事务和 TCP 配置事务互斥;清除配置或重启均先触发安全释放。factory_reset 仅由用户显式恢复出厂操作触发,不自动用于恢复失败。首次配对 psk 必须是 16 字节,不能用空值创建配对配置。

该 UART 禁止输出普通文本日志及输入回显;应用日志使用其他调试通道或独立日志构建。ROM/Bootloader 在重启时可能产生文本,视为噪声并按第 5.2.2 节重同步,不能把 Magic 扫描当作日志复用机制。客户端超时不得盲目重发 cfg_set/factory_reset;改用状态读取及 pending 恢复流程。手动救援提供 Go CLI,与 Web UI 共用 serialcfg 库。

### 4.2 客户端串口配网流程

选择串口 → info_get 获取默认/现有脱敏配置 → 填写完整配置(含 WiFi 密码) → 生成随机 16 字节 PSK(crypto/rand) → 原子保存 pending 记录 → cfg_set → cfg_result 成功后等待自动重启 → 使用新 IP/PSK 完成 TCP 认证并核对非敏感配置 → 标记 ready。串口库使用 go.bug.st/serial。

应答丢失时保留旧配置和 pending 新配置,先尝试新 TCP 认证或重新读取串口状态,不得重新生成 PSK、自动恢复出厂或覆盖旧设备记录。串口请求总等待期限为 3 s,超时只代表结果未知;串口传输完成不等于持久化成功。

### 4.3 网络参数

- 固件使用 `esp_netif_dhcpc_stop` + `esp_netif_set_ip_info` 设置静态 IP。
- `esp_wifi_set_ps(WIFI_PS_NONE)` 关闭省电;TCP 开启 `TCP_NODELAY` 与 keepalive;固定信道更佳。
- 客户端只保存设备列表:名称、IP、端口、PSK(`Challenge` 中的 MAC/名称/固件版本仅用于 Web UI 显示,不做校验)。
- 修改 IP 后设备重启,客户端按新 IP 重连。

### 4.4 会话规则

- **同一时刻只允许一个已认证会话**。已有会话时,新连接收到 `Challenge` 后直接关闭,**不踢掉**已认证会话。
- 未认证连接从 accept 起总超时 2 s,不因收到零碎字节延长。最多一个握手中的连接;监听处理不得阻塞活动会话。
- 心跳每 1 s 一次(双向),**3 s 无完整且认证有效的帧视为断线**。独立单调时钟看门狗触发释放,不依赖 `recv_exact` 返回;零碎字节、无效帧不能续期。TCP keepalive 仅为辅助。
- 未配对(`paired=0`)的设备拒绝一切输入帧。

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

`Msg.body` 必须存在,方向和阶段必须合法:设备先发 Challenge,客户端只回 Auth,设备只回 AuthOk/AuthFail;客户端验证 AuthOk 后才发送加密消息。认证后 C→D 允许输入、release_all、heartbeat、cfg_get、cfg_set、reboot;D→C 允许 heartbeat、status、cfg_data、cfg_result。解码失败、空 body、未知消息类型、非法方向或越界值均断开并释放;已知消息内的未知字段可忽略。nonce、HMAC、MAC 必须验证精确长度,缺失字段不能用全零值代替有效数据。协议版本不等于 1 时拒绝,不自动降级。

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
2. C→D  Auth{ nonce_c[16], hmac[32] }                                     明文, seq=0
        hmac  = HMAC-SHA256(PSK, "EK-AUTH-C" | transcript_hash | nonce_c)
3. D→C  AuthOk{ hmac[32] }                                                明文, seq=0
        hmac2 = HMAC-SHA256(PSK, "EK-AUTH-D" | transcript_hash | nonce_c)
4. 双方派生:key = HMAC-SHA256(PSK, "EK-KEY" | transcript_hash | nonce_c)[0:16]
5. 此后所有帧 flags.bit0=1,使用 AES-128-GCM,每方向 seq 从 0 开始
```

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

### 5.4 ek.proto

```proto
syntax = "proto3";
package ek;

message Empty {}

// 握手(明文)
message Challenge {
  uint32 proto_ver = 1;
  bytes  nonce_d   = 2;   // 16B
  uint32 fw_ver    = 3;   // 0x00MMmmpp
  bytes  mac       = 4;   // 6B
  bool   paired    = 5;
  string name      = 6;
}
message Auth     { bytes nonce_c = 1; bytes hmac = 2; }  // 16B, 32B
message AuthOk   { bytes hmac = 1; }                     // 32B
message AuthFail { uint32 reason = 1; }

// 输入(加密)
message KbState {
  uint32  mods    = 1;   // bit0..7: LCtrl,LShift,LAlt,LGui,RCtrl,RShift,RAlt,RGui
  fixed64 keys_lo = 2;   // bit n = HID Usage n (0..63)
  fixed64 keys_hi = 3;   // bit n = HID Usage 64+n (64..127)
  fixed64 keys_128_191 = 4;
  fixed64 keys_192_255 = 5; // 0xE0..0xE7 只通过 mods 表示,此处必须为 0
}
message Mouse {
  uint32 buttons = 1;    // bit0..4: L,R,M,Back,Forward
  sint32 dx      = 2;
  sint32 dy      = 3;
  sint32 wheel   = 4;
  sint32 hwheel  = 5;
}
message Consumer { uint32 usage = 1; } // 完整状态:0=释放;初版最多同时一个 Consumer Usage

// 状态与配置(加密)
message Status {
  uint32 fw_ver      = 1;
  uint32 uptime_s    = 2;
  bool   usb_mounted = 3;
  uint32 kb_leds     = 4;   // bit0 Num, bit1 Caps, bit2 Scroll
  sint32 rssi        = 5;
}
message Config {
  string name     = 1;
  string ssid     = 2;
  string password = 3;
  bytes  psk      = 4;   // 空 = 不修改;CFG_DATA 返回时恒为空
  uint32 usb_vid  = 5;
  uint32 usb_pid  = 6;
  uint32 ip       = 7;   // 数值形式: 192.168.1.10 = 0xC0A8010A
  uint32 gw       = 8;
  uint32 mask     = 9;
  uint32 tcp_port = 10;
}

message ConfigResult {
  uint32 code = 1; // 0=已持久化,1=非法字段,2=存储失败,3=忙
  uint32 field = 2; // 非法字段的 Config 字段号,0=无
}

// 串口信封:与 TCP Msg 分开,复用配置消息,不开放输入控制
message SerialInfo {
  uint32 fw_ver = 1;
  bytes mac = 2; // 6B
  bool paired = 3;
  Config config = 4; // psk/password 恒为空
}
message SerialAck {
  uint32 code = 1; // 0=成功,1=不支持版本,2=非法请求,3=存储失败,4=忙
}
message SerialPacket {
  uint32 proto_ver = 1; // 1
  reserved 2; // request_id 统一放在 ek_hdr_t.seq,不在 body 重复存储
  oneof body {
    Empty info_get = 16;
    SerialInfo info = 17;
    Config cfg_set = 18;
    ConfigResult cfg_result = 19;
    Empty reboot = 20;
    Empty factory_reset = 21;
    SerialAck ack = 22;
  }
}

message Msg {
  oneof body {
    Challenge challenge = 1;
    Auth      auth      = 2;
    AuthOk    auth_ok   = 3;
    AuthFail  auth_fail = 4;

    KbState   kb          = 16;
    Mouse     mouse       = 17;
    Consumer  consumer    = 18;
    Empty     release_all = 19;

    Empty     heartbeat = 32;
    Status    status    = 33;

    Empty     cfg_get   = 48;
    Config    cfg_data  = 49;
    Config    cfg_set   = 50;   // 完整替换;只有空 psk 表示保留
    Empty     reboot    = 51;
    ConfigResult cfg_result = 52;
  }
}
```

### 5.5 ek.options(nanopb)

```
ek.Challenge.nonce_d  max_size:16  fixed_length:true
ek.Challenge.mac      max_size:6   fixed_length:true
ek.Challenge.name     max_size:32
ek.Auth.nonce_c       max_size:16  fixed_length:true
ek.Auth.hmac          max_size:32  fixed_length:true
ek.AuthOk.hmac        max_size:32  fixed_length:true
ek.Config.name        max_size:32
ek.Config.ssid        max_size:33
ek.Config.password    max_size:65
ek.Config.psk         max_size:16
ek.SerialInfo.mac     max_size:6 fixed_length:true
```

### 5.6 Protobuf 使用要点

- 固件用 **nanopb**,禁用 callback 字段与动态分配;`Msg` 与收发缓冲区按任务/会话独占静态分配,不放任务栈;禁止跨任务共享可变解码对象。
- proto3 零值不编码:键盘全释放编码为空 `KbState`,解码得到全 0,语义正确。
- 键位图用四个 `fixed64`,覆盖 0..255;0..3 保留位及 0xE0..0xE7 对应位必须为零。mods 只允许低 8 位,鼠标 buttons 只允许低 5 位。Consumer Usage 使用实现公布的支持列表,不支持项不转发。
- 有符号数一律 `sint32`(ZigZag),负的 `dx/dy` 只占 1~2 字节。
- `psk` 不设 `fixed_length`:解码后判断 `size == 0 || size == 16`。
- 鼠标四轴内部用 `int64` 累加并检查加法溢出;每帧每轴限制在 ±1000000,越界拒绝。发送协议时分段为合法 sint32 值,发送 HID 时按描述符范围分段,只扣除已成功提交的量。
- `Empty` 消息在 `oneof` 中保留存在性,用于 `heartbeat` / `release_all` 等。

## 6. 固件设计

任务划分:

| 任务 | 职责 |
|---|---|
| `wifi_task` | STA 连接、静态 IP、断线重连 |
| `serial_task` | 共享帧头/CRC/SerialPacket 解析、配置/重启/恢复出厂,复用配置服务 |
| `tcp_task` | Accept、握手、GCM、nanopb 解码、入队 |
| `hid_task` | 维护键盘/鼠标状态,合并鼠标 delta,按 USB 轮询节拍发送 HID 报告 |
| NVS | WiFi 凭据、IP、端口、名称、PSK、USB VID/PID |

行为要点:

- **安全释放**:TCP 断开、心跳超时、队列溢出、USB 卸载、收到 `release_all` 时执行第 6.1 节释放屏障,覆盖键盘、鼠标和 Consumer。
- 认证前收到加密类消息,或加密后收到握手类消息,一律断开。
- 支持 `CFG_SET` 完整替换配置(空 `psk` 表示不改),按第 6.2 节提交后重启。
- 可选:RGB LED 指示状态,BOOT 键恢复出厂。

### 6.1 USB 交付与释放屏障

- 状态转换使用容量为 128 的有界 FIFO,不能只保存最后状态。按下与抬起即使在一个 USB 轮询周期内到达也必须分别提交。只在同一按钮状态的连续段中合并位移,禁止跨按钮边界合并。队列项保存值副本,不得引用可被下次解码覆盖的 Msg。
- HID endpoint ready 时提交队头,提交成功后等待完成回调再推进;失败不扣除位移。1 ms 为轮询目标,不是网络或端到端延迟保证。跨接口按 FIFO 顺序完成提交,明确区分已入队、已提交及 USB 传输完成。
- 释放屏障通过独立高优先级控制状态触发,不得排在满队列尾部:废弃旧输入和位移、清零逻辑状态,等待在途报告完成后优先提交各接口全释放。屏障完成前不提交新输入;断线时同时使旧会话队列失效。
- 队满或最老输入等待超过 100 ms 时关闭会话并触发屏障。USB 不可用时无法保证实际送达释放,记录 pending-release;重新枚举后首先发送全释放。释放前不接受输入。
- 独立看门狗检查认证有效帧时间,到 3 s 时触发屏障并关闭会话。网络任务不能因队列或 USB 等待阻止看门狗运行。

### 6.2 配置事务

- CFG_SET 完整替换所有非 PSK 字段,零值不是“不修改”;只有 psk 长度 0 表示保留。客户端先 CFG_GET 后编辑完整快照;开放网络使用空 password。设备返回 CFG_DATA 时 psk 恒为空,密码默认脱敏;客户端必须要求用户重新填写密码后才能提交完整替换,不得把脱敏值当作真实密码写回。
- 校验名称 UTF-8 最多 31 字节、SSID 1..32 字节、密码为空或 8..63 字节口令或 64 位十六进制值、PSK 0/16 字节、端口 1..65535、VID/PID 1..65535、连续掩码及主机/网关地址合法性。所有字段先校验,失败不改变活动或持久配置。USB VID/PID 只能采用有合法使用依据的值。
- 同时只允许一个配置事务,串口与 TCP 共用互斥锁。候选配置以带版本和校验的单个 NVS blob 保存,采用双槽及递增版本号;完整写入并 commit 后才作为有效版本,启动选择最新完整有效槽。断电不能产生字段混合。
- 提交成功后先触发输入释放,发送 CFG_RESULT(code=0),等待发送完成或最多 200 ms 后重启;失败返回错误且不重启。客户端每会话最多一个 CFG_SET 在途,不自动重发,不允许切换后仍给旧连接写配置。
- 客户端发送前原子落盘旧配置与 pending 新配置;应答丢失或连接断开不等于失败。尝试新 IP/PSK 并认证,读取非敏感配置核对后标为 ready;失败时保留两份候选供恢复,不无界循环改配置。CFG_RESULT 成功只代表持久化成功,不代表新 WiFi 一定可达;错误网络参数通过串口恢复。

## 7. 客户端设计(Go)

源码目录以 [源码目录与 WSL 开发约定](source-layout.md) 为准:Go 客户端采用单 module,程序入口放 sources/host/cmd,实现放 sources/host/internal;设备源码放 sources/device,共享协议源放 sources/protocol。

### 7.1 输入捕获与拦截

| 平台 | 捕获 | 拦截 |
|---|---|---|
| Windows | Raw Input + LL 钩子(线程 `LockOSThread` 并跑消息循环) | 钩子回调返回 1 吞掉事件;远程模式下 `ClipCursor` 锁定本机光标。钩子回调中**只入队,不做网络 IO** |
| Linux | `/dev/input/event*` | 正常运行时 `EVIOCGRAB` 独占物理设备,本地模式通过 `uinput` 回注,异常时解除抓取 |

限制:Windows 涉及完整性级别和安全桌面的行为必须分场景验证,远程控制管理权限窗口时按需提权;不承诺拦截 `Ctrl+Alt+Del`、`Win+L`。Linux 需要输入设备和 `/dev/uinput` 权限(提供最小权限 udev 规则与安装脚本)。

- Windows 钩子必须同步决定吞放,不能先交给异步 dispatcher 再决定。钩子线程维护最小路由/热键状态,按事件序号与 dispatcher 衔接,回调只非阻塞入队。键盘和按钮以钩子为唯一来源,Raw Input 只取相对位移及滚轮,避免双计数;验证低级钩子吞键后 Raw Input 的实际可用性。回注带自定义标记并排除,避免递归;退出、会话锁定或切换桌面时撤销 ClipCursor 并回 LOCAL。
- Linux 按 SYN_REPORT 处理批次,EV_KEY value=2 不重复改变状态;监听热插拔、排除本程序 uinput 设备、处理 SYN_DROPPED 并通过 EVIOCG* 重建状态。回注设备声明所需能力及 SYN_REPORT。多个物理设备同按一个键时使用每设备状态与引用计数,拔出设备释放其贡献。
- Linux LOCAL 仍抓取并回注,“回退本机”不等于正常运行时关闭 EVIOCGRAB。回注失败、输入队列溢出或退出时关闭抓取 FD;进程崩溃后物理设备恢复本地输入,虚拟设备销毁。

### 7.2 发送策略

- 位移和滚轮仅在相同按钮状态的相邻段内累加,约 **1 ms** 尝试取走一次;按钮边界封存此前移动段。合并只能减少未写入 TCP 的数据,不能撤回内核缓冲区的数据,不承诺完全消除“鼠标回放”。
- **键盘和鼠标按键不合并**,必须按序发送;键盘状态每次变化立即发送;鼠标按键变化立即 flush。
- 热路径复用缓冲区与 `Msg` 对象,用 `MarshalAppend` 序列化后原地 `Seal`,避免 GC 压力。
- 原地加密使用 `gcm.Seal(pt[:0], nonce[:], pt, hdr)`;完全分离的 dst 也合法,禁止不完全重叠。
- 读端套 `bufio.NewReader`;写端头、密文、tag 在同一缓冲区一次 `Write`。
- 每会话一个 reader、一个 writer 及生命周期管理器;所有输入/心跳/配置只由 writer 编码、分配 seq 并写入。处理短写;部分写入后失败立即断开,不得在同一连接复用 seq 重试。
- 应用发送队列最多 128 条,最老输入队龄上限 100 ms,每次写入总期限 100 ms。队满、超龄或写超时关闭会话并回 LOCAL,不静默丢失状态转换。dispatcher 和钩子不得等待网络写入。阈值是初始实施值,经实测可调整并记录依据。

## 8. 热键与统一切换状态机

默认热键为左右 Ctrl 的 HID Usage 集合 `{0xE0, 0xE4}`,支持 Web UI 录入至少两个键的其他组合。物理按键状态与已转发状态分开维护;不得因切换清空物理状态。

### 8.1 热键语义

- 组合最后一个键按下,且没有组合外的键或鼠标按钮按住时触发一次。自动重复不触发;所有组合键均释放后才重新武装。录入模式不执行切换。
- 明确取舍:不延迟普通 Ctrl 操作,因此首个 Ctrl 可短暂被当前目标看到。只吞掉完成组合的按下以及后续组合键事件,并释放旧目标已收到的 Ctrl。不能承诺整个热键零泄漏,具有单 Ctrl 动作的软件可能观察到它。
- 触发后等待组合键全部释放,期间不转发键盘、鼠标和 Consumer 输入。期间新按下的键/按钮加入抑制集合,直到各自物理释放才解除;位移直接丢弃。无在线目标时仍完成本次组合的吞键及释放,保持 LOCAL。

### 8.2 目标与轮换

目标为 `LOCAL, DEV0, DEV1, ...`,按设备列表顺序循环,仅包含已启用、已认证且 USB 已挂载的设备。重排按稳定设备 id 保持目标。Web UI 可直接选择目标,与热键共用以下流程;不提供反向或直接跳转热键。

活动设备断线、USB 卸载、删除或禁用时立即回 LOCAL;自动重连成功后仍保持 LOCAL。非活动设备只收发心跳、状态和配置,不接收输入。

### 8.3 统一切换流程

唯一 dispatcher 串行处理输入和切换,拥有目标、物理状态、已转发状态、鼠标累加量及 generation。步骤如下:

1. 增加 generation,进入切换抑制状态;将当前按住的键/按钮加入抑制集合,保留物理状态,清除待发送位移。切换期间新按下的键/按钮也加入集合,不把旧拖拽或修饰键继承到新目标。
2. 旧目标为本机时,通过 SendInput/uinput 补发其已收到的键、按钮释放。旧目标为远程时,writer 丢弃未开始编码的旧代输入并优先写 release_all;在途 Write 不能撤回,必须在其 100 ms 期限内完成,随后发送释放,失败则关闭连接。dispatcher 异步接收结果,仍能处理输入或取消。
3. 旧目标释放帧写出或连接已关闭后再提交新目标。写出不等于 USB 已释放;网络故障下旧设备可能保持按键直到 3 s 看门狗到期,界面不得显示“已确认释放”。故障回退 LOCAL 不等待旧网络写入完成,立即撤销 ClipCursor 和远程吞键模式。
4. 新远程目标须再次检查就绪状态,先由其 writer 写 release_all 再开放当前代输入,失败回 LOCAL。远程到远程期间保持本机输入拦截,不回注本机。
5. 切换完成后允许新的普通输入,抑制集合中的输入仍等待各自释放;热键切换还须等组合键全部释放。任何切换来源均执行同样的状态清理。

每条排队输入带本地 generation 和会话实例标识,writer 编码前再次验证,旧代数据或旧连接回调不得污染新会话。每设备保留独立 Conn、GCM、seq、缓冲区和队列。切换请求串行执行,进行中只保留最后一个用户目标请求;LOCAL/故障回退优先取消远程切换。

每设备独立生命周期管理器负责连接、握手、并行收发及重连,退避为 1 s、2 s、5 s 并加入随机抖动。输入队列溢出视为状态失步,关闭活动连接并恢复本机输入;Linux 先解除抓取,同步物理状态后才允许恢复抓取转发。

## 9. Web UI 与安全

- 仅绑定 `127.0.0.1:62626`,前端静态资源 `go:embed`。
- **绑定回环地址不足以防护**,必须:校验 `Host` 为 `127.0.0.1:62626`;校验 `Origin`;启动时生成随机 token,所有写操作和 WebSocket 都要携带。
- 功能:设备列表(添加/删除/启用/禁用/拖动排序)、热键设置、设备配置读写、串口配网、状态面板(在线状态、USB 枚举状态、RSSI、最后有效帧距今时间、键盘 LED)。当前 Empty 心跳不能测量 RTT,不显示伪造的 RTT 指标。
- 多设备:显示**当前控制目标**(LOCAL 或某台设备)并高亮;每台设备提供"切换到此设备"按钮(与热键等效,走同一个 dispatcher);轮换顺序可视化;活动设备断线回退时给出提示。
- 本地配置文件中设备为数组:`id`、`name`、`ip`、`port`、`psk`、`enabled`;顺序即轮换顺序。
- 客户端配置文件含 PSK:Linux 文件权限 `0600`、目录 `0700`;Windows 放 `%APPDATA%` 下并设置仅当前用户及系统可读写的 ACL。临时文件和 pending 备份沿用同样权限,使用原子替换,日志不输出敏感字段。
- Origin 精确匹配 `http://127.0.0.1:62626`,拒绝跨源及 null Origin 的写请求/WS;无 Origin 的本地工具请求仍必须提供 token。token 由同源初始页面获取,不放 URL 或日志,不启用宽泛 CORS。敏感读取接口也要求 token,GET 无副作用。

## 10. 开发环境与流程

具体目录、生成代码、构建产物及 Windows/WSL 衔接见 [源码目录与 WSL 开发约定](source-layout.md)。

```
WSL2 (Ubuntu 22.04/24.04)
 ├─ ESP-IDF v5.2+(~/esp)   idf.py build → build/*.bin、flash_args
 ├─ CMake/Ninja 自动调用 nanopb + protoc 生成 ek.pb.c/h
 └─ Go 工具链(CMake/Ninja 自动生成 ek.pb.go 后构建 host)

Windows
 ├─ 烧录:flash.ps1 读取打包 manifest,调用 Windows esptool
 │        (打包后通过 WSL UNC 路径读取自包含烧录包)
 └─ 调试日志:独立通道或禁用配网服务的日志构建;配网 UART 不运行文本 monitor
```

- 工程放 WSL 内部文件系统,编辑用 VS Code + WSL 扩展 + Espressif IDF 扩展。
- 提供 WSL 构建/打包入口与 Windows flash.ps1;烧录包使用相对路径,不直接把含 Linux 路径的 flash_args 传给 Windows。
- 暂不使用 `usbipd-win`(ESP32-S3 烧录时 USB 重枚举,重新绑定麻烦)。
- CI:GitHub Actions,固件用 `espressif/esp-idf-ci-action`,客户端交叉编译 Go;通过 CMake/Ninja 依赖 sources/protocol/ek.proto 自动生成并编译两端代码,验证协议一致。
- 锁定 ESP-IDF、esp_tinyusb、nanopb、protoc、Go 和第三方库版本,不用无上限的 `5.2+` 作为实际依赖。生成 Go 代码时提供稳定 go_package 或生成器 M 参数。CI 额外执行跨语言字节向量测试,仅两端编译成功不能证明互通。含 CGO 的平台依赖使用目标平台 runner 构建。
- 实机联调在本地进行,日志与报错贴回对话定位。

## 11. 里程碑

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
| 本地 UI 安全 | 错误 Host/Origin、缺失 token、跨源 WebSocket、敏感未授权读取均拒绝;日志无 PSK/密码 |

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

## 13. 下一步

1. 完成 M0 平台实验,并从本文输出 `ek.proto` / `ek.options` / `protocol.h` 及 CMake/Ninja 自动生成规则、跨语言协议向量。
2. 固件工程:`CMakeLists.txt`、`idf_component.yml`、`sdkconfig.defaults`、TinyUSB 复合 HID 描述符、串口 Protobuf 配网、静态 IP WiFi STA、TCP Server + 握手 + GCM + nanopb + `hid_task`。
3. Go 客户端:协议层 → 输入捕获与热键 → Web UI。
