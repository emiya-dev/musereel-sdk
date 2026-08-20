# musereel-sdk

[English](README.md) | [简体中文](README.zh-CN.md)

`musereel-sdk` 是面向受控 MuseReel workbench 实例，以及拥有或运营这些实例的
后端的 Go SDK 边界。本仓库仍为私有仓库，且 **This repository is intentionally
private until the public-release decision is made.**

这里所说的“公开 SDK”是 S31 定义的“给第三方后端使用的边界”，不是宣布仓库对外
发布。本轮不做任何对外发布或托管声明。

## 安装

模块路径是：

```sh
go get github.com/emiya-dev/musereel-sdk
```

仓库仍是私有的，因此执行该命令需要调用方配置正常的私有模块访问权限。模块声明
使用 Go `1.25`。

## 快速开始

[`example_test.go`](example_test.go) 里的示例是经过编译器看住的起点。它们只使用
SDK 导出 API，在内存中现场生成签名密钥，并使用占位配置。

每个示例都带 `// Output:` 指令——正是这条指令让 `go test` **真的去跑**它，
而不只是编译它。这是刻意的：只被编译的示例只能证明 API 名字存在，
证明不了这段代码能用。`ExampleGatewayCreateRequest` 会对它展示的请求调用
`Validate`，所以示例里的请求一旦不合法就会让包测试变红，而不是把照抄它的
读者带进坑里。

也正因为它们真的会跑，示例绝不建立网络连接、不读真实证书、不依赖环境变量。
`ExampleRequestFingerprint` 与 `ExampleCanonicalGatewayPath` 完全确定，
因此直接断言精确输出；其中那个指纹值把 JCS 规范化结果钉住了——
按 `CONTRIBUTING.md` 的纪律，它变了就是一次 breaking change。

示例覆盖以下第一步：

- `ExampleMTLSConfig` 展示本地 mTLS 文件配置。真实客户端把它传给
  `NewTLSConfig` 或 `NewMTLSCredentials`，再使用 `DialRuntime`。
- `ExampleNewEd25519Signer`、`ExampleNewEd25519SignerFromPEM`、
  `ExampleNewES256Signer` 和 `ExampleNewES256SignerFromPEM` 展示已登记的签名密钥形式。
- `ExampleNewToken` 与 `ExampleNewCachedTokenSource` 展示本地 `TokenSource`；真实 runtime 连接通常
  使用 `NewGRPCTokenSource`，通过 mTLS 身份交换短期 token。
- `ExampleNewAuthenticatedClient`、`ExampleNewGatewayClient`、
  `ExampleGatewayCreateRequest` 与 `ExampleNewRuntimeClient` 展示客户端面的构造和
  一个经过校验的请求，但不发请求。
- `ExampleRequestFingerprint`、`ExampleCanonicalGatewayPath` 与
  `ExampleSignAssertion`、`ExampleSignActorAssertion` 展示认证重试之间必须保持稳定的请求身份。

## API 概览

这个包刻意只做传输与控制面边界：

| 领域 | 主要导出 API |
| --- | --- |
| mTLS 与传输 | `MTLSConfig`、`NewTLSConfig`、`NewMTLSCredentials`、`DialRuntime` |
| Runtime token | `Token`、`NewToken`、`TokenSource`、`CachedTokenSource`、`GRPCTokenSource`、`NewCachedTokenSource`、`NewGRPCTokenSource`、`WithClock` |
| 断言与身份 | `Signer`、`NewEd25519Signer`、`NewEd25519SignerFromPEM`、`NewES256Signer`、`NewES256SignerFromPEM`、`AssertionInput`、`SignAssertion`、`SignActorAssertion`、`RequestFingerprint`、`CanonicalGatewayPath` |
| 已认证 gRPC | `AuthenticatedClient`、`NewAuthenticatedClient`、`AssertionCall` |
| Runtime 控制面 | `RuntimeClient`、`NewRuntimeClient`、`WithRuntimeAssertion`、类型化 `runtime.v1` 方法，以及 `RuntimeRPCError` |
| Gateway invocation 面 | `GatewayClient`、`NewGatewayClient`、`GatewayCreateRequest`、`GatewayInvocationSpec`、`CreateAsync`、`CreateStream`、`Get`、`GetWithETag`、`Cancel`、`DownloadArtifact`、`NewPoller` |
| 规范化 JSON | `github.com/emiya-dev/musereel-sdk/jcs` 子包的 `jcs.CanonicalizeJSON` |

生成的 protobuf 消息与 service client 位于 `runtime` 子包。`ExchangeRuntimeToken`
刻意藏在 `GRPCTokenSource` 后面；完成 token 交换后，`RuntimeClient` 才负责类型化
runtime 控制面。

## 核心概念

### mTLS bootstrap

`MTLSConfig` 指定客户端证书、私钥、CA bundle 和可选的 server name。
`NewTLSConfig` 校验当前密钥对，并在每次握手时重新读取密钥对，使原子替换文件即可
轮换证书。私钥只留在 TLS 栈内部，不会进入错误信息或格式化后的配置输出。
`DialRuntime` 使用这些传输凭据打开 gRPC 连接。

### Runtime token 与已认证 gRPC

`GRPCTokenSource` 通过 mTLS 连接发送生成的空
`ExchangeRuntimeTokenRequest`，并缓存服务端返回的短期 `Bearer` token。
`CachedTokenSource` 提供固定 60 秒刷新窗口和 single-flight 交换行为；`Token`
默认保持不透明，`NewToken` 可用于自定义本地 `TokenSource` 实现。交换请求不携带
tenant、instance 或 scope 字段。

`AuthenticatedClient` 为通用 unary gRPC 调用挂上 Bearer token，并且只在稳定码
`runtime_unauthenticated` 后精确重试一次。重试沿用相同的业务参数，保留调用方拥有的
幂等键和请求指纹。`RuntimeClient` 的类型化 runtime 方法使用这条边界。

### Actor assertion 与 JCS 指纹

`Signer` 支持已登记的 EdDSA 和 ES256 形式。`SignAssertion` 生成带新 nonce 的紧凑
JWS，最长有效窗口为 60 秒；`SignActorAssertion` 是只取传输值的便捷形式。
`AssertionInput` 的身份上下文包含已经绑定的 instance、tenant、session 和 actor。

`RequestFingerprint` 根据 method、规范路径、actor、幂等键和 JCS body，计算冻结的
SHA-256、无 padding 的 base64url 请求指纹。空 body 按 `{}` 规范化。`jcs` 子包实现
服务端的 RFC 8785 子集，并按 UTF-16 码元而不是 Go 的 UTF-8 `sort.Strings` 顺序
排序对象属性名。

### 幂等与 invocation 交付

变更调用使用调用方提供的幂等键；查询调用不携带幂等键。带 assertion 的重试会签发
新 nonce，但业务身份与指纹保持不变。`GatewayClient` 将 async 和 stream 创建拆成
不同方法，不把 `delivery_mode` 放进 create body，并提供带 ETag 的读取、取消、SSE
处理和带 `Content-Digest` 校验的产物下载。

`ResolveRegistrationRequest.domain` 由前端提供，并由 SDK 原样透传。SDK 不从 Host、
Origin 或配置推导、规范化或补全它。`invite_code` 是冻结 wire 字段，只有频道标识
语义。

## 契约同步

`contract-input/` 是冻结契约布局，只允许两类文件：

- **镜像（mirrors）**：Sluice 文件的副本。`runtime.proto` 是冻结 runtime 镜像，
  `frozen_public_error_codes.json` 是 Sluice 的
  `backend/service/gateway/frozen_public_error_codes.json` 副本。每一份镜像都必须由
  `scripts/check-contract-pin.sh` 做哈希校验。
- **钉记录（pin records）**：承载期望值，是门禁的输入而不是门禁对象：
  `SOURCE.txt` 与 `GATEWAY_HTTP_ANCHOR.txt`。它们不自哈希。不得有第三类文件。

`SOURCE.txt` 钉住 source repository、source path、source commit、SHA-256、freeze date，
以及固定的代码生成工具链元数据。

`contract-input/runtime.proto` 是冻结镜像，不是第二事实源。唯一事实源在 `sluice`
仓库。手改镜像是违规；刷新必须从钉住的 Sluice 源文件来，并且同时更新 source
commit、SHA-256 与 freeze date，作为一次受审改动提交。本地门禁会重算镜像 SHA-256，
不等于钉住值就失败；它不会拉取内部源仓库。

Gateway HTTP 面由
[`contract-input/GATEWAY_HTTP_ANCHOR.txt`](contract-input/GATEWAY_HTTP_ANCHOR.txt)
锚定。冻结的 Gateway 章节、source commit、路由数量和 freeze date 只在那个文件中
维护；请去那里读取，不要复制到 README。路由契约仍归 Sluice 所有，本仓只记录锚点，
不再复制第二份 HTTP 契约。

`contract-input/reference/jcs-server-reference.go.txt` 曾是“未哈希镜像”的反面教材：
它读起来像权威文件，却没有人负责保鲜。它用 `sort.Strings`（UTF-8 字节序）排序对象键，而
`jcs/jcs.go` 与线上 Sluice 实现 `backend/pkg/app/core/jcs.go` 都按 RFC 8785 §3.2.3
的 UTF-16 码元序排序。两者在非 BMP 属性名上结论不同；照那份漂移文件重写的人会
得到 `actor_assertion_invalid`，而且没有线索。该文件已经删除；JCS 的行为事实源是
`jcs/jcs.go` 加 `jcs_test.go` 里的 UTF-16 断言。

`runtime/runtime.pb.go` 与 `runtime/runtime_grpc.pb.go` 由冻结的
`contract-input/runtime.proto` 配合钉住的本地 protoc 工具链生成。
`ExchangeRuntimeToken` 使用生成的 protobuf 消息和标准 gRPC protobuf codec；手写的
过渡 codec 已删除，其 golden-byte 断言已迁移到生成类型。

## Conformance

Conformance 使用手动 `conformance` build tag。默认本地 check 使用
`go test -tags conformance -short ./...`；真实 compose 环境另跑：

```sh
go build -tags conformance ./...
go test -tags conformance ./conformance
```

必填环境变量为：

`MUSEREEL_CONFORMANCE_GATEWAY_URL`、
`MUSEREEL_CONFORMANCE_RUNTIME_TARGET`、
`MUSEREEL_CONFORMANCE_MTLS_CERT_FILE`、
`MUSEREEL_CONFORMANCE_MTLS_KEY_FILE`、
`MUSEREEL_CONFORMANCE_MTLS_CA_FILE`、
`MUSEREEL_CONFORMANCE_SIGNING_PRIVATE_KEY_FILE`、
`MUSEREEL_CONFORMANCE_SIGNING_KID`、
`MUSEREEL_CONFORMANCE_INSTANCE_ID`、
`MUSEREEL_CONFORMANCE_TENANT_ID`、
`MUSEREEL_CONFORMANCE_SESSION_ID`、
`MUSEREEL_CONFORMANCE_ACTOR`、
`MUSEREEL_CONFORMANCE_SKU_ID`、
`MUSEREEL_CONFORMANCE_TASK_REF`、
`MUSEREEL_CONFORMANCE_DELIVERY_MODE`（`async` 或 `stream`）。

可选环境变量为：

`MUSEREEL_CONFORMANCE_MTLS_SERVER_NAME`、
`MUSEREEL_CONFORMANCE_SPEC_SCHEMA_VERSION`、
`MUSEREEL_CONFORMANCE_SPEC_INPUT_JSON`、
`MUSEREEL_CONFORMANCE_SPEC_PARAMETERS_JSON`、
`MUSEREEL_CONFORMANCE_EVENT_ID`。

审核收据**故意没有对应的环境变量**，因为它没法从外部塞进来：harness 先跑一次
`moderation.generate.v1` 调用，再从那次调用的终态结果里把收据读出来
（`conformance.go` 的 `mintModerationReceipt`）。

`MUSEREEL_CONFORMANCE_SPEC_SCHEMA_VERSION` **没有单一默认值**。留空时由
`conformance.go` 的 `conformanceSchemaVersionBySKU` 提供：`video.generate.v1` 为 `3`，
其余六个 SKU 为 `1`。

产物 SKU 的 artifact ID **不通过环境变量提供**。契约要求 `{artifact_id}` 由服务端
签发，`invocation_artifact.id` 是随机 UUID，所以客户端预置的值不可能命中。harness
从终态 `snapshot.result` 解析 ID，再通过 SDK 下载接口校验 `Content-Digest`。

机器可读的 stdout 标记是：

```text
ARTIFACT_LEG=downloaded sku=<sku_id> count=<n>
ARTIFACT_LEG=skipped sku=<sku_id>
```

⚠ 单跑一个非产物 SKU 只会打 `skipped`——那证明不了 artifact 传输腿是通的。
`skipped` 对应非产物 text / lyrics / moderation SKU；`downloaded` 的 count 至少必须为
1。要覆盖这条腿，驱动矩阵必须让 `downloaded` 在 video / image / music / speech 上各出现一次。

目标是 Sluice 侧 compose 提供的 E14 fixture。缺环境时
`TestSluiceComposeConformance` 会 fail-fast，而不是 skip。**唯一的例外是 `-short`**：
它整条跳过 compose 腿，让同包离线单测进入默认 check。

## S31 边界

本 SDK 面向拥有或运营受控 workbench 实例的后端，也可面向第三方后端使用。不得嵌入
浏览器、移动端或客户可控前端。

SDK 边界可以承载认证材料、actor assertion、幂等和安全重试行为。ledger、pricing、
compliance、supplier 逻辑永久不属于 SDK，即使实现起来对调用方方便也不例外。SDK
不得提供任何绕过服务端校验的能力。

包内只有类型化的传输与控制面 wrapper。runtime client 对服务端给出的字符串金额与
单位只做透传，不做数值解释；ledger、pricing、compliance 和 supplier 决策仍在服务端。

## 本地门禁

运行完整本地基线：

```sh
./scripts/ci.sh check
```

门禁会检查格式，构建默认和带 conformance tag 的包，对两个构建面运行 vet，运行默认
测试与 short conformance 测试，并校验契约 pin。只检查 pin 的命令是：

```sh
./scripts/check-contract-pin.sh
```

本里程碑没有 hosted workflow；本地 shell gate 是当前唯一的 CI 形态。

## 项目状态与许可证

- Module：`github.com/emiya-dev/musereel-sdk`
- Go 语言版本：`1.25`
- Runtime 契约：`runtime.v1`，由 `contract-input/SOURCE.txt` 冻结
- SDK-002 提供 mTLS 装载与轮换、短期 runtime-token 缓存、actor assertion 和通用已认证
  gRPC 边界。
- SDK-003 负责 invocation wrapper。
- SDK-004 增加已提交的 protobuf/gRPC 生成代码和类型化 runtime 控制面 client。
- 外部依赖：grpc-go `v1.80.0`、protobuf `v1.36.11`，以及 `go.mod`/`go.sum` 中锁定的
  间接依赖闭包。
- License： [Apache-2.0](LICENSE)。版权持有人一行刻意留给 owner 在 `LICENSE` 中填写。
- Hosted CI/workflow wiring：TBD；本里程碑唯一的 CI 形态是本地 shell gate。
