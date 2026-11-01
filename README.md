# EspKVM

ESP32-S3 USB 键鼠共享器，Windows/Linux Go 客户端。实施基线见 [设计决议](docs/esp32-kvm-decisions.md) 和 [目录约定](docs/source-layout.md)。

**当前为协议与串口配网阶段，还不能进行完整键鼠共享。** 详细范围和未完项见 [实施状态](docs/implementation-status.md)。现有固件应用只启动串口配网。

## WSL host 构建

工作副本放在 WSL ext4，例如 `~/src/EspKVM`。保留现有 Windows 副本及未提交工作，自行选择迁移时机。准备 Go 1.27.3、Python 3.12（含 venv）、CMake >=3.24、Ninja、GCC 和 Git：

```sh
bash scripts/wsl/bootstrap.sh
export PATH="$PWD/out/tools/bin:$PWD/out/tools/proto/bin:$PATH"
cmake --preset host-linux -DPython3_EXECUTABLE="$PWD/out/tools/proto/bin/python"
cmake --build --preset host-linux --target host test-host test-protocol
```

生成文件不提交。干净检出先经 CMake 生成协议，再调用 Go；不要直接先执行 `go mod tidy`，否则尚不存在的 pb 包会被当成外部 module。可执行文件在 `out/bin`。

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
out/bin/espkvmctl ports
out/bin/espkvmctl -serial /dev/ttyUSB0 info
out/bin/espkvmctl -serial /dev/ttyUSB0 -id desk -config device.json serial-set
out/bin/espkvmctl -id desk verify
out/bin/espkvmctl -address 192.168.1.10:24800 -psk-file secret.hex check
```

参数放在子命令前。完整配置 JSON 遵循 `sources/protocol/ek.proto` 的 protobuf JSON 映射；必须重新填写 WiFi 密码，PSK 为 base64 的 16B 随机值。配置请求前会原子保存 pending 凭据；应答丢失保留 pending，不自动重发。`verify` 用新凭据认证并比对脱敏配置；当前固件尚未实现 TCP，需待后续固件集成后使用。

默认凭据文件：Linux 用户配置目录下 `EspKVM/config.json`（文件 0600，目录 0700）；Windows `%APPDATA%/EspKVM/config.json`（当前用户和 SYSTEM 的 ACL）。日志不输出凭据。

## 固件配网引导

准备 ESP-IDF **v5.2.3** 并运行其 `export.sh`，先运行 bootstrap 获取固定 nanopb runtime。然后：

```sh
cmake --preset wsl -DPython3_EXECUTABLE="$PWD/out/tools/proto/bin/python"
cmake --build --preset wsl --target device
```

配网固件已使用 ESP-IDF v5.2.3 编译通过，尚未烧录或实机验证。UART0 为 115200 8N1，应用/bootloader 日志关闭；ROM 启动日志由串口解析器视作噪声。不要在该串口混入应用文本日志。NVS 初始化异常直接失败，不自动擦除用户配置。

`cmake --build --preset wsl --target package-device` 从实际 `flasher_args.json` 提取地址/参数，并生成 `out/flash/<时间-标识>/manifest.json` 和 bin 文件。Windows 安装 `tools/requirements-flash.txt` 中固定的 esptool 后，执行：

```powershell
./scripts/windows/flash.ps1 -Package '\\wsl.localhost\Ubuntu-26.04\home\user\src\EspKVM\out\flash\<包目录>' -Port COM3
```

烧录脚本先验证 manifest 和每个文件的 SHA256，再把包复制到 Windows 临时目录调用 esptool。

硬件验收需要双 USB 的 ESP32-S3 开发板及 Windows/Linux/BIOS 目标机；普通测试不访问串口或抓取真实键鼠。
