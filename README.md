# EspKVM

ESP32-S3 USB 键鼠共享器，Windows/Linux Go 客户端。实施基线见 [设计决议](docs/esp32-kvm-decisions.md) 和 [目录约定](docs/source-layout.md)。

VS Code 任务列表只有 **build**、**rebuild**（`Ctrl+Shift+B` 默认 build）。选择任务后，再在第二级选择 **Host** 或 **Device**。Device 当前构建 M1 HID 测试固件；Host 同时构建 Windows / Linux 的 amd64 客户端工具，分别输出到 `build/host/bin/windows/`、`build/host/bin/linux/`。任务内部自动配置并按需生成 proto，不再单独显示 configure 任务。

Windows 通过系统 `wsl.exe` 调用构建脚本，不要求安装 Windows CMake。D 盘工作副本会同步到 WSL ext4 的固定缓存目录进行增量编译，完成后把产物放回工作区 `build/host/bin` 或 `build/device/flash/hid-selftest`；rebuild 保留 HID 配置。WSL Remote 中也使用相同两项任务。脚本识别本次已安装的 IDF/Go/Python 环境，也支持通过 `IDF_PATH`、`IDF_TOOLS_PATH`、`IDF_PYTHON_ENV_PATH`、`PROTO_PYTHON` 指定工具链。下面的 CMake 命令仍可独立使用。

输出目录按目标分开，共用生成工具放在 `build/tools`：

```text
build/
├── host/
│   ├── cmake/
│   └── bin/
│       ├── windows/             # .exe
│       └── linux/               # ELF
├── device/
│   ├── cmake/
│   ├── firmware/                # 配网固件 IDF 输出
│   ├── hid-selftest/            # M1 固件 IDF 输出
│   ├── config/                  # rebuild 保留的配置
│   ├── deps/
│   └── flash/
│       ├── firmware/            # 配网烧录包
│       └── hid-selftest/        # M1 烧录包
└── tools/
```

**当前为协议与串口配网阶段，还不能进行完整键鼠共享。** 详细范围和未完项见 [实施状态](docs/implementation-status.md)。现有固件应用只启动串口配网。

当前优先做 [M1 USB 硬件实验](experiments/usb-hid/README.md) 和 [最小 Windows M0](experiments/windows-input/README.md)，暂不冻结协议。[2026-11-01 设计修订](docs/design-review-2026-11-01.md) 记录 Linux LOCAL 被动读取、认证接管、optional 凭据、带 id 的心跳、6KRO 和 UI 会话方案。

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
build/host/bin/linux/espkvmctl ports
build/host/bin/linux/espkvmctl -serial /dev/ttyUSB0 info
build/host/bin/linux/espkvmctl -serial /dev/ttyUSB0 -id desk -config device.json serial-set
build/host/bin/linux/espkvmctl -id desk verify
build/host/bin/linux/espkvmctl -address 192.168.1.10:24800 -psk-file secret.hex check
```

参数放在子命令前。完整配置 JSON 遵循 `sources/protocol/ek.proto` 的 protobuf JSON 映射；必须重新填写 WiFi 密码，PSK 为 base64 的 16B 随机值。配置请求前会原子保存 pending 凭据；应答丢失保留 pending，不自动重发。`verify` 用新凭据认证并比对脱敏配置；当前固件尚未实现 TCP，需待后续固件集成后使用。

默认凭据文件：Linux 用户配置目录下 `EspKVM/config.json`（文件 0600，目录 0700）；Windows `%APPDATA%/EspKVM/config.json`（当前用户和 SYSTEM 的 ACL）。日志不输出凭据。

## 固件配网引导

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

烧录脚本先验证 manifest 和每个文件的 SHA256，再把包复制到 Windows 临时目录调用 esptool。

硬件验收需要双 USB 的 ESP32-S3 开发板及 Windows/Linux/BIOS 目标机；普通测试不访问串口或抓取真实键鼠。
