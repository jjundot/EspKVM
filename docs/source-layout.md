# 源码目录与 WSL 开发约定

本文规定实施时的目录边界。协议行为以 [设计决议](esp32-kvm-decisions.md) 及其 [2026-11-01 修订](design-review-2026-11-01.md) 为准；冲突时采用修订。目录及构建产物位置以本文为准。以下包含规划，未落地部分以实施状态为准。

## 仓库结构

采用单仓库,一个 ESP-IDF 工程、一个 Go module。工作副本放在 WSL ext4,例如 `~/src/EspKVM`,不用 `/mnt/c` 或 `/mnt/d` 作为编译工作目录。

```text
EspKVM/
├── README.md                      # 环境准备、构建、烧录、运行入口
├── CMakeLists.txt                 # 根构建入口,Ninja 调度 host/device/codegen
├── CMakePresets.json              # WSL 默认及 host 平台构建预设
├── cmake/                         # 协议生成、Go 构建和 IDF 子构建模块
├── .gitignore
├── .github/workflows/             # 协议检查、IDF 构建、Linux/Windows 构建
├── docs/
│   ├── esp32-kvm-decisions.md      # 协议与行为规范
│   └── source-layout.md           # 本文
├── sources/                      # 仅放项目源码与协议定义
│   ├── protocol/
│   │   ├── ek.proto                   # 唯一消息定义
│   │   ├── ek.options                 # nanopb 字段上限
│   │   ├── wire-format.md             # 实现阶段拆出的帧布局/状态机规范
│   │   └── testdata/                  # C/Go 共用字节向量与期望结果
│   ├── device/
│   │   ├── CMakeLists.txt             # ESP-IDF 项目入口
│   │   ├── sdkconfig.defaults         # S3、USB、网络和任务默认配置
│   │   ├── partitions.csv
│   │   ├── dependencies.lock          # 锁定 IDF 组件依赖
│   │   ├── main/
│   │   │   ├── CMakeLists.txt
│   │   │   ├── idf_component.yml      # 声明 TinyUSB/nanopb 等依赖
│   │   │   └── app_main.c             # 组装组件、启动任务,不承载业务逻辑
│   │   └── components/
│   │       ├── protocol/           # 共享头部编解码、CRC、字段校验、生成消息
│   │       │   ├── CMakeLists.txt
│   │       │   ├── include/           # 对外 frame.h 等接口
│   │       │   ├── src/
│   │       │   └── test/              # 帧/CRC/消息校验及跨语言向量测试
│   │       ├── config/             # 配置校验、NVS 事务,串口/TCP 共用
│   │       ├── wifi/               # STA、静态 IP、重连
│   │       ├── serial/             # UART、Magic 重同步、配网请求分发
│   │       ├── session/            # TCP、认证、GCM、seq、心跳、看门狗
│   │       └── hid/                # TinyUSB 描述符、FIFO、释放屏障
│   └── host/
│       ├── go.mod
│       ├── go.sum
│       ├── cmd/
│       │   ├── espkvm/main.go         # 常驻客户端与 Web UI
│       │   └── espkvmctl/main.go      # 配网/诊断 CLI,复用同一业务库
│       └── internal/
│           ├── protocol/
│           │   ├── pb/ek.pb.go        # 构建自动生成,忽略不提交
│           │   ├── frame.go           # 同一 12B header 编解码
│           │   ├── crc.go
│           │   └── validate.go
│           ├── session/               # TCP 握手/GCM/收发/生命周期
│           ├── serialcfg/             # 串口帧读取、重同步、配网事务
│           ├── input/                 # 平台无关事件接口、每设备状态聚合
│           │   ├── input_windows.go   # Windows 钩子、Raw Input、回注
│           │   └── input_linux.go     # evdev、grab、uinput、热插拔
│           ├── keymap/                # 显式扫描码/KEY_* 到 HID Usage 映射
│           ├── switcher/              # 唯一 dispatcher 与切换状态机
│           ├── config/                # 客户端配置、pending 事务、权限
│           └── web/
│               ├── server.go          # HTTP/WS、Host/Origin/token 校验
│               ├── embed.go           # go:embed assets/*
│               └── assets/            # 初版原生 HTML/CSS/JS,不引入前端构建链
├── scripts/
│   ├── wsl/
│   │   ├── bootstrap.sh           # 检查/准备固定版本开发环境
│   │   ├── check-toolchain.sh     # 检查固定版本生成器与工具链
│   │   └── package-device.sh    # 生成 Windows 可直接烧录的包
│   └── windows/
│       ├── flash.ps1              # 在 Windows 调用 esptool 操作 COM
│       └── build-host.ps1       # Windows 原生依赖构建/验证入口
├── tools/
│   ├── versions.env               # IDF、Go、protoc、生成器版本
│   └── requirements-proto.txt     # 固定 nanopb/protobuf 生成依赖
├── packaging/
│   ├── linux/                    # udev 规则、安装/卸载脚本
│   └── windows/                  # 分发与安装资源
├── experiments/
│   ├── windows-input/            # M0 平台验证,不被正式程序依赖
│   └── linux-input/
└── build/                        # 所有可重建产物,不提交
    ├── host/
    │   ├── cmake/                # 客户端 CMake/Ninja
    │   └── bin/
    │       ├── windows/          # Windows amd64 可执行文件
    │       └── linux/            # Linux amd64 可执行文件
    ├── device/
    │   ├── cmake/                # 设备 CMake 编排
    │   ├── firmware/             # IDF 配网固件及生成的 C 协议
    │   ├── hid-selftest/          # 独立 M1 IDF 构建
    │   ├── config/               # 重建保留的配置
    │   ├── deps/                 # 固件第三方源码
    │   └── flash/                # firmware/、hid-selftest/ 烧录包
    └── tools/                    # 共用生成工具
```

固件各组件按需采用 `include/`、`src/`、`test/` 结构。Go 测试放在被测包旁,不要建立重复客户端源码的顶层 tests 树。需要实机的验收单独标记,不能让普通单元测试依赖串口或管理员权限。

## 模块依赖边界

- `protocol` 只负责 wire 格式、生成消息和结构校验,不访问 UART、socket、NVS、USB 或平台输入。
- 固件 `serial` 与 `session` 都调用 `protocol` 和 `config`;TCP 输入通过 `hid` 的入队/释放接口交付,不能直接操作 TinyUSB。
- `config` 不反向依赖传输层。调用方负责发结果和安排重启,避免配置组件内直接重启导致应答丢失。跨组件释放/重启由应用层协调。
- Go `switcher` 依赖输入及会话的窄接口,测试可注入假设备/时钟;web 和 CLI 调用业务服务,不直接改 target、写 Conn 或递增 seq。
- 平台文件使用 `_windows.go`/`_linux.go` 和必要 build tags 隔离,不拆成需要互相导入的同名子包。初版只支持 Windows/Linux,其他平台显式报告不支持。

共享 header 是“同一字段布局和字节向量”,不是让 Go 导入 C 头文件。C/Go 分别实现小端编解码,由共同 testdata 防止漂移。消息定义只编辑 sources/protocol/ek.proto;文档中的代码块是规范示例,落地后由链接引用实际文件,避免长期维护多份 schema。

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

1. 以 WSL 内 `~/src/EspKVM` 为唯一 Git 工作副本,Windows 编辑器通过 WSL Remote 打开。当前 D 盘目录暂不自动迁移或删除,迁移时需保留未提交工作及 Git 状态。
2. WSL 构建后打包 bootloader、partition table、应用等实际烧录所需 bin,并生成 manifest:包含芯片、偏移、相对文件名、flash 参数和校验值。参数从实际 IDF 构建元数据提取,不手写固定偏移。
3. Windows flash.ps1 接收包目录和 COM 端口,通过 UNC 路径读取包,必要时复制至 Windows 临时目录。它只消费已完成的构建产物,不用 Windows esptool 直接解析带 WSL 路径的原始 flash_args。
4. Windows 默认负责物理 COM 配网及烧录。WSL 无串口访问时可使用 Windows espkvmctl;不为首次配网强制引入 usbipd。

优先落地 sources/protocol/、sources/device/main、sources/device/components/protocol、sources/host/internal/protocol 和 CMake 生成规则,再逐步补组件;不提前创建只有空壳的全部目录。
