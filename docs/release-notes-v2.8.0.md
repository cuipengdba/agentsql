# AgentSQL v2.8.0 发布说明

状态：发布候选。正式发布须以 CI、签名资产、升级恢复、官网和公网 Demo 的完成记录为准。

## 本版范围

- 为 Milvus 2.6.25 指定 TLS 单机 `default` 数据库增加 Agent MCP `search_vectors`。调用方提交数据源 ID、精确 collection、FloatVector 字段、一个有限 float32 向量、显式标量字段和 1–20 的 `top_k`；不提交原生表达式或管理命令。
- 网关每次重新核验 Agent、租户、当前数据源、精确 `default.collection` 表授权，以及向量字段和全部输出字段的列白名单。受控路径拒绝明文、无认证、`root` 和信任任意证书配置；连接验证 CA、主机名与服务器版本。
- 执行前核对集合结构、向量维度与 COSINE 索引；结果受请求时限、命中数、字节数和来源字段脱敏约束。持久预检审计成功后才连接 Milvus，持久终态审计成功后才交付结果。原始向量及标量值不写入审计。
- 旧 Milvus 管理预览仍为连接级能力。受控 TLS 数据源的管理端连接测试改走新路径；旧明文配置不会获得 `search_vectors` 权限。

## 验证边界

Milvus 2.6.25 官方单机镜像、合成数据、目标 collection 的最小 Search 角色、真实 HTTP MCP、MCP Inspector 0.16.8 和 Dify MCPClient 1.17.1 的允许/拒绝调用已通过。错误证书、维度不匹配、越权集合/字段、撤权、结果脱敏、持久审计，以及独立审计库和 Milvus 停机后的失败关闭与恢复也已验证；详见[候选验收](v2.8.0-acceptance.md)。

其他 Milvus 版本、集群/复制、混合检索、写入、生产证书轮换、长期负载、生产数据和完整生产支持未验证。公网 Demo 继续使用合成 PostgreSQL 数据。
