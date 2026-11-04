# 源码目录与 WSL 开发约定

本文规定目录边界，下面的目录树与当前仓库一致。协议与行为以 [v11 设计决议](esp32-kvm-decisions.md) 为唯一基线；[2026-11-01 修订](design-review-2026-11-01.md) 已全部并入 v11，仅作归档。目录及构建产物位置以本文为准。

## 仓库结构

采用单仓库,一个 ESP-IDF 工程、一个 Go module。工作副本可放在 WSL ext4(例如 `~/src/EspKVM`);放在 `/mnt/d` 时 `scripts/wsl/build.sh` 会把源码同步到 `~/.cache/esp-kvm/vscode/<hash>/source` 再编译,结果同步回 `build/`。

```text
EspKVM/
├── README.md                      # 环境准备、构建、烧录、运行入口
├── CMakeLists.txt                 # 根构建入口,Ninja 调度 host/device/codegen 及原生测试
├── CMakePresets.json              # host / device 两个预设
├── cmake/
│   ├── Protocol.cmake             # nanopb / protoc-gen-go 生成规则
│   ├── ProtocolVectors.cmake      # 原生 C 测试、PSA 向量、C 会话与 Go 互通测试
│   └── BuildHost.cmake            # Go 客户端与探针构建
├── .github/workflows/             # device.yml、host.yml、interop.yml
├── docs/
│   ├── esp32-kvm-decisions.md     # 协议与行为规范(唯一基线)
│   ├── source-layout.md           # 本文
│   ├── keep-awake.md              # 防休眠与睡眠目标机切换
│   ├── implementation-status.md   # 实施状态
│   ├── validation-2026-11-02.md   # 验收记录
│   └── design-review-2026-11-01.md # 已并入 v11 的修订归档
├── sources/
│   ├── protocol/
│   │   ├── ek.proto               # 唯一消息定义
│   │   ├── ek.options             # nanopb 字段上限
│   │   ├── wire-format.md         # 帧布局/状态机摘要
│   │   └── testdata/              # frames.txt(串口帧)、v2.txt(握手/GCM 向量)
│   ├── device/
│   │   ├── CMakeLists.txt         # ESP-IDF 项目入口
│   │   ├── sdkconfig.defaults
│   │   ├── partitions.csv
│   │   ├── dependencies.lock      # 锁定 IDF 组件依赖
│   │   ├── main/app_main.c        # 组装组件、启动服务,不承载业务逻辑
│   │   └── components/
│   │       ├── protocol/          # frame.c(头部/CRC/串口解析器)、ek_crypto.c(PSA)、message_validate.c
│   │       ├── config/            # 配置校验、NVS 双槽事务,串口/TCP 共用;test/ 含 FreeRTOS/NVS 桩
│   │       ├── serial/            # serial_service.c(配网请求)、serial_log.c(日志封帧)
│   │       ├── network/           # WiFi STA 静态 IP、TCP accept、握手、GCM、心跳、看门狗;test/ 为 POSIX 适配
│   │       ├── hid_reports/       # 可移植核心:报告编码器、hid_engine(FIFO/屏障)、keep_awake
│   │       └── hid_service/       # TinyUSB 描述符与回调,驱动 hid_engine
│   └── host/
│       ├── go.mod / go.sum
│       ├── cmd/espkvm/
│       │   ├── main.go            # 常驻客户端与 Web UI;ctl 子命令入口
│       │   ├── browser_*.go       # 平台浏览器启动
│       │   ├── ui/                # go:embed 的原生 HTML/CSS/JS
│       │   └── app.ico/app.png/app_windows_amd64.syso
│       └── internal/
│           ├── protocol/          # frame.go(头部/CRC/串口解析器)、crypto.go、validate.go
│           │   └── pb/ek.pb.go    # 构建生成,忽略不提交
│           ├── session/           # TCP 握手/GCM/writer/释放屏障/RTT
│           ├── serialcfg/         # 串口单请求事务、日志帧读取
│           ├── input/             # 平台无关事件、物理状态/引用计数、热键、鼠标合并
│           ├── capture/           # source_windows.go(钩子/Raw Input/回注)、source_linux.go(evdev/grab)、gate.go
│           ├── hostlock/          # HOST 会话锁屏状态(Windows WTS、Linux logind)
│           ├── keymap/            # 扫描码/KEY_* 到 HID Usage 映射
│           ├── switcher/          # 唯一 dispatcher 与切换状态机
│           ├── devices/           # 每设备生命周期、重连、配置请求
│           ├── config/            # 客户端配置、pending 事务、Web 密码、文件权限与锁
│           ├── control/           # Web/CLI 共用的业务操作
│           ├── web/server.go      # HTTP API、登录/会话、Host/Origin/CSRF、SSE
│           ├── cli/               # espkvm ctl
│           ├── service/           # install/uninstall/start/stop/status：systemd 单元、登录计划任务、udev 规则
│           └── debuglog/
├── scripts/
│   ├── wsl/                       # bootstrap、check-toolchain、build、build-snapshot、package-device、verify-host
│   └── windows/                   # flash.ps1、build-host.ps1
├── tools/                         # versions.env、generate.py、generate_vectors.cjs、package_device.py 及依赖清单
├── experiments/
│   ├── usb-hid/                   # M1 独立 IDF 工程
│   ├── windows-input/             # M0 Windows 探针及原始记录
│   └── linux-input/               # M0 Linux 探针
└── build/                         # 所有可重建产物,不提交
    ├── host/bin/                  # 客户端可执行文件
    ├── device/                    # firmware/、hid-selftest/、config/、deps/、flash/
    └── tools/                     # 共用生成工具
```

固件各组件按需采用 `include/`、`src/`、`test/` 结构。Go 测试放在被测包旁,不要建立重复客户端源码的顶层 tests 树。需要实机的验收单独标记,不能让普通单元测试依赖串口或管理员权限。

## 模块依赖边界

- `protocol` 只负责 wire 格式、生成消息和结构校验,不访问 UART、socket、NVS、USB 或平台输入。
- 固件 `serial` 与 `network` 都调用 `protocol` 和 `config`;TCP 输入通过 `hid_service` 的入队/释放接口交付,不直接操作 TinyUSB。`hid_reports` 不依赖 ESP-IDF,可原生测试。
- `config` 不反向依赖传输层。调用方负责发结果和安排重启,避免配置组件内直接重启导致应答丢失。跨组件释放/重启由应用层协调。
- Go `switcher` 依赖输入及会话的窄接口,测试可注入假设备/时钟;web 和 CLI 经 `control`/`devices` 调用业务服务,不直接改 target、写 Conn 或递增 seq。
- 平台文件使用 `_windows.go`/`_linux.go` 和必要 build tags 隔离,不拆成需要互相导入的同名子包。初版只支持 Windows/Linux,其他平台显式报告不支持。

共享 header 是"同一字段布局和字节向量",不是让 Go 导入 C 头文件。C/Go 分别实现小端编解码,由共同 testdata 防止漂移。消息定义只编辑 sources/protocol/ek.proto;文档中的代码块是规范示例,避免长期维护多份 schema。

## 生成与构建约定

根目录 CMake 是统一编排入口,使用 Ninja;ESP-IDF 仍保留独立 CMake 工程,由子构建调用,不把 IDF 的工具链与宿主 Go 构建混到同一 CMake project。根项目不启用 ESP 交叉编译器,通过 IDF 子构建目标产生设备固件。host-only 预设不要求安装 IDF。

- sources/protocol/ek.proto 和 ek.options 是唯一输入。cmake/Protocol.cmake 定义生成规则,使用 add_custom_command(OUTPUT ...) 显式列出 ek.pb.c、ek.pb.h、ek.pb.go;生成只在构建阶段执行,不在 configure 中用 execute_process 生成。
- DEPENDS 包含 proto、options、导入的 proto、生成规则文件和工具版本清单,并追踪所选 protoc/插件可执行文件。使用绝对路径和 VERBATIM,先创建输出目录;工具缺失或生成失败直接终止构建,不得继续使用旧输出。
- device 的 nanopb 输出写入 IDF 二进制目录下 generated/protocol,不提交。生成的 .c 加入 protocol 组件源文件,生成目录加入 include 路径;包含 .pb.h 的消费者必须有显式目标依赖,避免首次并行构建先编译后生成。
- host 的 Go 输出写入 sources/host/internal/protocol/pb/ek.pb.go,因 Go module 内导入路径固定,该派生文件放源码树但由 .gitignore 忽略,不提交、不手改。根构建的 host 目标必须依赖 Go 协议生成目标,然后才执行 go build/go test;直接 go build 不是干净检出的受支持首次构建入口。
- 根构建与单独 idf.py build 共用同一个 CMake 生成函数:root 负责 host Go 生成,IDF 子构建负责 device C 生成,一个输出只能有一个生产者。仅构建 device 不要求 Go/protoc-gen-go,仅构建 host 不要求 IDF/nanopb 插件。
- 同一工作副本的 host 生成使用进程锁并以临时文件原子替换,仅内容变化时更新文件;生成输出位置不依赖 GOOS/GOARCH。多个平台构建不能并发覆盖不同版本生成结果,需要不同生成器版本时使用独立工作副本。clean 删除构建派生文件,不得删除手写源码。
- 根 host 构建目标每次调用 go build,由 Go 缓存负责增量,不单凭最终 exe 时间戳跳过,从而正确追踪 Go 源文件、go.mod/go.sum 和 embed 静态资源变更。协议输入未变时不重新调用 protoc。
- 固定 protoc、nanopb、protoc-gen-go、IDF、Go 版本。Go module 地址和 go_package 在初始化时统一确定,只保留一个 sources/host/go.mod,不引入 go.work。
- CI 从干净工作副本执行 CMake configure + Ninja,验证首次构建、无变更构建、修改 proto/options 后重新生成及并行依赖;再执行 C/Go 字节向量测试。生成结果不提交,不再采用“检查已提交生成代码漂移”的流程。
- build/、生成的 Go 文件、managed_components、机器 sdkconfig 和工具虚拟环境忽略;sdkconfig.defaults、依赖锁文件及协议输入提交。ref/、tmp/ 维持本地参考用途,不作为构建依赖。

仅保留按产物命名的 `host`、`device` 两个预设；运行平台由构建入口处理，不再作为预设名称：

| 入口 | 用途 |
|---|---|
| cmake --preset host | 配置客户端构建，build/host/cmake，无需 IDF |
| cmake --build --preset host | 自动生成 Go 协议并编译客户端工具 |
| cmake --build --preset host --target test-host test-protocol | 运行非硬件测试 |
| cmake --preset device | 配置设备构建，build/device/cmake，无需 Go |
| cmake --build --preset device | 当前阶段默认构建独立 M1 HID 测试固件 |
| cmake --build --preset device --target device | 自动生成 nanopb C 协议并编译配网固件 |
| cmake --build --preset device --target package-device | 打包配网固件到 build/device/flash/firmware |
| cmake --build --preset device --target package-hid-selftest | 打包 M1 固件到 build/device/flash/hid-selftest |

构建预设使用仓库相对路径,通过 CMake 自身源码目录定位 sources/,不依赖调用者 cwd。纯 Go 的 Windows 交叉编译由入口设置 GOOS/GOARCH，不增加平台预设；含 CGO 则使用 Windows runner/本机。交叉编译成功不能替代 Windows 输入行为验证。

## Windows 与 WSL 的衔接

1. 当前 Git 工作副本在 D 盘(`D:\workspace\EspKVM`)。`scripts/wsl/build.sh build device|host` 在 WSL 中用 rsync 把源码同步到 `~/.cache/esp-kvm/vscode/<hash>/source` 编译,烧录包和客户端再同步回本仓库 `build/`。生成器 Python 默认取 `build/tools/proto/bin/python`,不可用时通过 `PROTO_PYTHON` 指定(如 `~/.local/opt/ek/proto312/bin/python`)。也可以把工作副本直接放在 WSL ext4 中。
2. WSL 构建后打包 bootloader、partition table、应用等实际烧录所需 bin,并生成 manifest:包含芯片、偏移、相对文件名、flash 参数和校验值。参数从实际 IDF 构建元数据提取,不手写固定偏移。
3. Windows flash.ps1 接收包目录和 COM 端口,通过 UNC 路径读取包,必要时复制至 Windows 临时目录。它只消费已完成的构建产物,不用 Windows esptool 直接解析带 WSL 路径的原始 flash_args。
4. Windows 默认负责物理 COM 配网及烧录。WSL 无串口访问时可使用 Windows `espkvm ctl`;不为首次配网强制引入 usbipd。


## 构建、烧录与 CLI 速查

以下内容从 README 移入。

- **VS Code 任务**：只有 build / rebuild 两项（`Ctrl+Shift+B` 默认 build），再选 Host 或 Device。Host 产出 `build/host/bin/espkvm_{x86_64_windows,x86_64_linux,aarch64_linux}.exe`；Device 产出正式固件和 M1 固件的烧录包。Windows 通过 `wsl.exe` 调用 `scripts/wsl/build.sh`，不需要 Windows CMake。
- **WSL 首次准备**：`bash scripts/wsl/bootstrap.sh`，需要 Go 1.27.3、Python 3.12、CMake ≥ 3.24、Ninja、GCC、ESP-IDF v6.0.3。proto 由 CMake 自动生成；首次构建前不要运行 `go mod tidy`。
- **Windows 客户端构建**：`./scripts/windows/build-host.ps1 -Test`（需先安装 `tools/requirements-proto.txt` 及 `protoc-gen-go@v1.36.6`）。
- **烧录**：安装 `tools/requirements-flash.txt` 中的 esptool 后，执行 `./scripts/windows/flash.ps1 -Package <build/device/flash/firmware/包目录> -Port COM3`；脚本复制后校验 manifest 和 SHA-256，全部匹配才烧录。
- **原生测试**：设置 `EK_PSA_SOURCE` 为 ESP-IDF 的 `components/mbedtls/mbedtls/tf-psa-crypto`，`--target test-protocol` 运行 C 向量、HID 引擎、NVS 模型以及 C 会话与 Go 客户端的互通测试；Linux 可加 `-DEK_SANITIZERS=ON`。
- **CLI**（参数放在子命令前）：

  ```sh
  espkvm ctl ports
  espkvm ctl -serial COM5 info
  espkvm ctl -serial COM5 serial-log -duration 30s
  espkvm ctl -serial COM5 -id desk -config device.json serial-set
  espkvm ctl pending-list
  espkvm ctl -id desk verify
  espkvm ctl -address 192.168.1.10:24800 -psk-file secret.hex check
  ```

  配置 JSON 使用 ek.proto 的 protobuf JSON 映射。首次配置必须同时提供 password（开放网络传显式空字符串）和 base64 编码的 16 字节 PSK。
- **凭据文件**：客户端可执行文件旁的 `config.json`（Windows ACL 只允许当前用户和 SYSTEM，Linux 为 0600）。
- **自启动**：发布物只有一个可执行文件，`install` 在原位置注册它，config.json 仍在其旁边；移动可执行文件后需重新 `install`。

  ```sh
  espkvm                          # 前台运行
  espkvm install                  # Linux 用 sudo；Windows 用管理员终端
  espkvm start|stop|restart|status
  espkvm uninstall
  espkvm help                     # 全部命令与参数
  ```

  - Linux：写入 systemd 系统单元 `espkvm.service`（开机启动，登录界面也可切换）和输入设备 udev 规则。服务以可执行文件所在目录的属主运行，附加 `espkvm` 与 `dialout`/`uucp` 组；只有该目录可写。日志：`journalctl -u espkvm -f`。前台运行同样需要先 `install` 一次以获得 udev 规则，并重新登录取得组权限。
  - Windows：注册登录触发的计划任务 `EspKVM`（最高权限、无窗口、失败自动重启）。低级键鼠钩子只在用户交互会话里有效，所以不做成 Windows 服务；锁屏、UAC 安全桌面期间无法捕获。`stop` 通过命名事件让进程正常退出，不需要管理员。
  - 自启动时不自动打开浏览器，直接访问 `http://127.0.0.1:62626` 用密码登录。
