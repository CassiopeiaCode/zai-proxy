# GitHub Actions Workflows

## Docker Build Workflow

该工作流自动构建并发布 Docker 镜像到 GitHub Container Registry (GHCR)。

### 触发条件

- **Push to main**: 当代码推送到 `main` 分支时
- **Tag creation**: 当创建版本标签时 (格式: `v*.*.*`)
- **Pull Request**: 当创建针对 `main` 分支的 PR 时（仅构建，不推送）
- **Manual trigger**: 可以在 GitHub Actions 页面手动触发

### 功能特性

- ✅ 多平台支持：`linux/amd64` 和 `linux/arm64`
- ✅ 自动标签管理：
  - `latest` - 最新的 main 分支构建
  - `v1.2.3` - 完整版本号
  - `v1.2` - 主要.次要版本号
  - `v1` - 主要版本号
- ✅ GitHub Actions 缓存优化构建速度
- ✅ 自动生成构建来源证明（artifact attestation）
- ✅ PR 构建验证（不推送镜像）

### 镜像地址

构建的镜像会发布到：
```
ghcr.io/cassiopeiacode/zai-proxy:latest
ghcr.io/cassiopeiacode/zai-proxy:v1.0.0
```

### 使用方法

#### 拉取最新镜像

```bash
docker pull ghcr.io/cassiopeiacode/zai-proxy:latest
```

#### 拉取特定版本

```bash
docker pull ghcr.io/cassiopeiacode/zai-proxy:v1.0.0
```

#### 运行容器

```bash
docker run -p 8000:8000 ghcr.io/cassiopeiacode/zai-proxy:latest
```

### 发布新版本

要发布新版本，只需创建并推送一个版本标签：

```bash
git tag v1.0.0
git push origin v1.0.0
```

这将自动触发工作流，构建并推送带有以下标签的镜像：
- `ghcr.io/cassiopeiacode/zai-proxy:v1.0.0`
- `ghcr.io/cassiopeiacode/zai-proxy:v1.0`
- `ghcr.io/cassiopeiacode/zai-proxy:v1`
- `ghcr.io/cassiopeiacode/zai-proxy:latest`

### 权限说明

工作流使用以下权限：
- `contents: read` - 读取仓库内容
- `packages: write` - 推送 Docker 镜像到 GHCR

这些权限通过 `GITHUB_TOKEN` 自动提供，无需额外配置。
