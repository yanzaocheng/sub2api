-- 用户对话记录：保存网关转发的每一轮对话（用户输入 + 模型回复），供管理后台查看。
-- 默认不写入任何数据，需在「系统设置 → 功能开关」中手动开启。
-- 设计约束：
--   1. 不设外键：用户 / API Key 被删除后历史对话仍然保留，邮箱与 Key 名称以快照形式存放
--   2. 每条记录只保存本轮新增的输入与模型回复；同一场对话的各轮通过 conversation_key 关联
--   3. 各文本字段在写入前已按字符数截断，单条记录体积有上限
CREATE TABLE IF NOT EXISTS conversation_records (
    id               BIGSERIAL PRIMARY KEY,
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    request_id       VARCHAR(128) NOT NULL DEFAULT '',
    conversation_key VARCHAR(64)  NOT NULL,
    user_id          BIGINT,
    user_email       VARCHAR(255) NOT NULL DEFAULT '',
    api_key_id       BIGINT,
    api_key_name     VARCHAR(255) NOT NULL DEFAULT '',
    group_id         BIGINT,
    endpoint         VARCHAR(64)  NOT NULL DEFAULT '',
    model            VARCHAR(255) NOT NULL DEFAULT '',
    stream           BOOLEAN      NOT NULL DEFAULT FALSE,
    duration_ms      BIGINT       NOT NULL DEFAULT 0,
    client_ip        VARCHAR(64)  NOT NULL DEFAULT '',
    user_agent       VARCHAR(255) NOT NULL DEFAULT '',
    system_prompt    TEXT         NOT NULL DEFAULT '',
    messages         JSONB        NOT NULL DEFAULT '[]'::jsonb,
    response         TEXT         NOT NULL DEFAULT '',
    prompt_preview   VARCHAR(255) NOT NULL DEFAULT '',
    response_preview VARCHAR(255) NOT NULL DEFAULT ''
);

-- 保留期清理与按时间范围筛选
CREATE INDEX IF NOT EXISTS idx_conversation_records_created_at
    ON conversation_records (created_at);
-- 读取 / 删除 / 聚合一场对话（按发起时间排序）
CREATE INDEX IF NOT EXISTS idx_conversation_records_conversation
    ON conversation_records (conversation_key, created_at, id);
-- 按用户筛选
CREATE INDEX IF NOT EXISTS idx_conversation_records_user
    ON conversation_records (user_id, created_at);
