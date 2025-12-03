# zai-proxy

zai-proxy 是一个基于 Go 语言的代理服务，将 z.ai 网页聊天转换为 OpenAI API 兼容格式。用户使用自己的 z.ai token 进行调用。

## 功能特性

- OpenAI API 兼容格式
- 支持流式和非流式响应
- 支持多种 GLM 模型
- 支持思考模式 (thinking)
- 支持联网搜索模式 (search)
- 支持多模态图片输入
- **自动生成签名**
- **自动更新签名版本号**

## 快速开始

### 安装运行

```bash
# 克隆项目
git clone https://github.com/kao0312/zai-proxy.git
cd zai-proxy

# 安装依赖
go mod download

# 运行服务
go run main.go
```

### Docker 部署

```bash
# 构建镜像
docker build -t zai-proxy .

# 运行容器
docker run -p 8000:8000 zai-proxy

# 使用环境变量
docker run -p 8000:8000 -e PORT=8080 -e LOG_LEVEL=debug zai-proxy

# 使用 HTTP 代理
docker run -p 8000:8000 -e HTTP_PROXY=http://proxy.example.com:8080 zai-proxy
```

### Docker Compose 部署

```bash
# 启动服务
docker-compose up -d

# 查看日志
docker-compose logs -f

# 停止服务
docker-compose down
```

编辑 `docker-compose.yml` 文件以配置环境变量和代理设置。

## 环境变量

| 变量名 | 说明 | 默认值 |
|--------|------|--------|
| PORT | 监听端口 | 8000 |
| LOG_LEVEL | 日志级别 | info |
| HTTP_PROXY | HTTP代理地址 (可选) | 空 |

HTTP_PROXY 支持的格式示例：
- `http://proxy.example.com:8080`
- `http://username:password@proxy.example.com:8080`
- `https://proxy.example.com:8443`

## 获取 z.ai Token

1. 登录 https://chat.z.ai
2. 打开浏览器开发者工具 (F12)
3. 切换到 Application/Storage 标签
4. 在 Cookies 中找到 `token` 字段
5. 复制其值作为 API 调用的 Authorization

## 支持的模型

| 模型名称 | 上游模型 |
|----------|----------|
| GLM-4.5 | 0727-360B-API |
| GLM-4.6 | GLM-4-6-API-V1 |
| GLM-4.5-V | glm-4.5v |
| GLM-4.5-Air | 0727-106B-API |

### 模型标签

模型名称支持以下后缀标签（可组合使用）：

- `-thinking`: 启用思考模式，响应会包含 `reasoning_content` 字段
- `-search`: 启用联网搜索模式

示例：

- `GLM-4.6-thinking`
- `GLM-4.6-search`
- `GLM-4.6-thinking-search`

## 使用示例

### curl 测试

```bash
curl http://localhost:8000/v1/chat/completions \
  -H "Authorization: Bearer YOUR_ZAI_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "GLM-4.6",
    "messages": [{"role": "user", "content": "hello"}],
    "stream": true
  }'
```

### 多模态请求：

```json
{
  "model": "GLM-4.5-V",
  "messages": [
    {
      "role": "user",
      "content": [
        {"type": "text", "text": "描述这张图片"},
        {"type": "image_url", "image_url": {"url": "https://example.com/image.jpg"}}
      ]
    }
  ]
}
```

### 支持的图片格式：
- HTTP/HTTPS URL
- Base64 编码 (data:image/jpeg;base64,...)