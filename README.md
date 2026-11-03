# EspKVM

ESP32-S3 USB 键鼠共享器，Windows/Linux Go 客户端。实施基线见 [设计决议](docs/esp32-kvm-decisions.md) 和 [目录约定](docs/source-layout.md)。

VS Code 任务列表只有 **build**、**rebuild**（`Ctrl+Shift+B` 默认 build）。选择任务后，再在第二级选择 **Host** 或 **Device**。Device 当前构建 M1 HID 测试固件；Host 构建三种客户端：Windows x86_64、Linux x86_64、Linux aarch64，统一输出到 `build/host/bin/`。文件名包含平台和架构；CLI 配置命令已集成在客户端的 `ctl` 子命令中。测试程序统一输出到 `build/test/`。任务内部自动配置并按需生成 proto，不再单独显示 configure 任务。

Windows 通过系统 `wsl.exe` 调用构建脚本，不要求安装 Windows CMake。D 盘工作副本会同步到 WSL ext4 的固定缓存目录进行增量编译，完成后把产物放回工作区 `build/host/bin` 或 `build/device/flash/hid-selftest`；rebuild 保留 HID 配置。WSL Remote 中也使用相同两项任务。脚本识别本次已安装的 IDF/Go/Python 环境，也支持通过 `IDF_PATH`、`IDF_TOOLS_PATH`、`IDF_PYTHON_ENV_PATH`、`PROTO_PYTHON` 指定工具链。下面的 CMake 命令仍可独立使用。

输出目录按目标分开，共用生成工具放在 `build/tools`：

```text
build/
├── host/
│   ├── cmake/
│   └── bin/
│       ├── espkvm_x86_64_windows.exe  # Windows 客户端（带图标）
│       ├── espkvm_x86_64_linux.exe    # Linux x86_64 客户端
│       └── espkvm_aarch64_linux.exe   # Linux ARM64 客户端
├── test/                        # 协议/输入等测试程序
├── device/
│   ├── cmake/
│   ├── firmware/                # 正式固件 IDF 输出
│   ├── hid-selftest/            # M1 固件 IDF 输出
│   ├── config/                  # rebuild 保留的配置
│   ├── deps/
│   └── flash/
│       ├── firmware/            # 正式固件烧录包
│       └── hid-selftest/        # M1 烧录包
└── tools/
```

**当前为协议 v2 候选与集成验收阶段，尚未完成发布验收。** 正式固件已接入 WiFi/TCP/认证/GCM/HID；常驻客户端、安全 API 和嵌入式 Web UI 已实现，仍待桌面及硬件验收。详细范围见 [实施状态](docs/implementation-status.md) 和 [验收记录](docs/validation-2026-11-02.md)。未配对固件只启动 UART 配网。

唯一设计基线为 [v11 决议](docs/esp32-kvm-decisions.md)。优先完成 [M1 USB 实验](experiments/usb-hid/README.md) 和 [Linux M0](experiments/linux-input/README.md) 的真实硬件验收，再冻结协议。VID/PID 默认 0，必须提供合法标识。

## WSL host 构建

工作副本放在 WSL ext4，例如 `~/src/EspKVM`。保留现有 Windows 副本及未提交工作，自行选择迁移时机。准备 Go 1.27.3、Python 3.12（含 venv）、CMake >=3.24、Ninja、GCC 和 Git：

```sh
bash scripts/wsl/bootstrap.sh
export PATH="$PWD/build/tools/bin:$PWD/build/tools/proto/bin:$PATH"
cmake --preset host -DPython3_EXECUTABLE="$PWD/build/tools/proto/bin/python"
cmake --build --preset host
cmake --build --preset host --target test-host test-protocol
```

proto 已纳入 CMake 默认构建：不需要手动运行 protoc、Python 生成脚本或先构建 proto 目标。普通构建会先生成 Go 协议代码，再构建 host；启用固件时也会先生成 nanopb C/H，再编译固件。修改 `.proto`/`.options` 后自动重新生成，无修改时跳过；也可显式构建 `--target proto`。生成文件不提交。不要在首次 CMake 构建前直接执行 `go mod tidy`，否则尚不存在的 pb 包会被当成外部 module。可执行文件在 `build/host/bin`。

当前 D 盘副本可使用 `bash scripts/wsl/build-snapshot.sh` 把源码复制到新的 WSL ext4 临时工作目录后构建。该便利脚本使用本次会话准备的 Python 环境，并打印快照路径；常规开发使用上面的标准入口。

## Windows host 构建

准备相同版本 Go、Python 与生成依赖、protoc-gen-go、CMake/Ninja，以及支持 Go race 的 C 编译器。把生成插件加入 PATH：

```powershell
python -m pip install -r tools/requirements-proto.txt
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.6
./scripts/windows/build-host.ps1 -Test
```

## CLI

```sh
build/host/bin/espkvm_x86_64_linux.exe ctl ports
build/host/bin/espkvm_x86_64_linux.exe ctl -serial /dev/ttyUSB0 info
build/host/bin/espkvm_x86_64_linux.exe ctl -serial /dev/ttyUSB0 -id desk -config device.json serial-set
build/host/bin/espkvm_x86_64_linux.exe ctl pending-list
build/host/bin/espkvm_x86_64_linux.exe ctl -id desk pending-discard
build/host/bin/espkvm_x86_64_linux.exe ctl -id desk verify
build/host/bin/espkvm_x86_64_linux.exe ctl -address 192.168.1.10:24800 -psk-file secret.hex check
```

设备日志以帧格式输出在配网串口上，用 `espkvm ctl -serial COM5 serial-log` 查看（`-duration 30s` 限定时长），普通串口终端只能看到乱码。参数放在子命令前。完整配置 JSON 遵循 `sources/protocol/ek.proto` 的 protobuf JSON 映射；首次配置须同时提供 password（开放网络显式空字符串）及 base64 的 16B 随机 PSK；已配对时缺省两字段表示保留，显式空 PSK 拒绝。配置请求前会原子保存 pending 凭据；应答丢失保留 pending，不自动重发。`verify` 用新凭据认证并比对脱敏配置；TCP 服务已实现，仍需真实设备互通验收。

默认凭据文件：客户端可执行文件旁的 `config.json`。Windows 文件 ACL 限制为当前用户和 SYSTEM；Linux 文件权限为 0600。首次升级时会从旧用户配置目录复制现有配置到可执行文件旁，并保留旧文件作为备份。Web UI 首次登录密码为 `admin`，请登录后立即修改；密码以 `md5("espkvm_" + 密码)` 的十六进制保存在 `web_password_md5` 字段中，修改后所有旧会话失效，只能用新密码访问。Web UI 显示客户端版本，格式为 `shortsha_YYYYMMDDTHHMMSSZ`；设备固件使用相同格式，但由设备固件独立编译生成。日志不输出凭据。

## 固件（未配对时仅 UART）

准备 ESP-IDF **v6.0.3** 并运行其 `export.sh`，先运行 bootstrap 获取固定 nanopb runtime。然后：

```sh
cmake --preset device -DPython3_EXECUTABLE="$PWD/build/tools/proto/bin/python"
cmake --build --preset device --target device
```

配网固件早期曾使用 ESP-IDF v5.2.3 编译；该分支现已 EOL，本轮迁移到 v6.0.3，实机验证仍待接板。已有旧 SDK 的 IDF 二进制目录须 fullclean 后重建，不直接复用旧工具链缓存。UART0 为 115200 8N1，应用/bootloader 日志关闭；ROM 启动日志由串口解析器视作噪声。不要在该串口混入应用文本日志。NVS 初始化异常直接失败，不自动擦除用户配置。

`cmake --build --preset device --target package-device` 从实际 `flasher_args.json` 提取地址/参数，并生成 `build/device/flash/firmware/<时间-标识>/manifest.json` 和 bin 文件。Windows 安装 `tools/requirements-flash.txt` 中固定的 esptool 后，执行：

```powershell
./scripts/windows/flash.ps1 -Package '\\wsl.localhost\Ubuntu-26.04\home\user\src\EspKVM\build\device\flash\firmware\<包目录>' -Port COM3
```

烧录脚本先验证 manifest，复制到 Windows 临时目录后检查复制文件的 SHA256，全部匹配才调用 esptool。

硬件验收需要双 USB 的 ESP32-S3 开发板及 Windows/Linux/BIOS 目标机；普通测试不访问串口或抓取真实键鼠。

## 本机 C/Go 互通与 Linux 安装

设置 EK_PSA_SOURCE 为已锁定 ESP-IDF 的 components/mbedtls/mbedtls/tf-psa-crypto 目录，CMake test-protocol 会增加 nanopb/PSA 向量、NVS 模型、同源 C TCP 与 Go 的互通测试。Linux 可加 -DEK_SANITIZERS=ON 检查便携 C 核心。原始 Protobuf/GCM 向量用 node tools/generate_vectors.cjs 生成；测试不连接真实设备。

Linux 构建后，可审阅并执行 sudo bash packaging/linux/install.sh USER，客户端安装到该用户的 `~/.local/bin/espkvm`，使其能在可执行文件旁创建配置文件；同时安装仅对专用 espkvm 组开放输入设备的 udev 规则，重新登录后生效。卸载用 sudo bash packaging/linux/uninstall.sh USER，不删除用户凭据。不默认安装常驻系统服务，客户端在用户桌面会话运行。
