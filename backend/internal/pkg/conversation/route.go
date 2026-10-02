package conversation

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// FormatForRoute 返回承载聊天式对话的网关路由所用的协议。
// fullPath 是 gin 的路由模板（c.FullPath()），不是请求的实际路径。
// 不承载对话的路由（count_tokens、embeddings、图片 / 视频、/responses 子路径等）返回 false。
func FormatForRoute(fullPath string) (Format, bool) {
	switch fullPath {
	case "/v1/messages", "/antigravity/v1/messages":
		return FormatAnthropic, true
	case "/v1/chat/completions", "/chat/completions":
		return FormatOpenAIChat, true
	case "/v1/responses", "/responses", "/backend-api/codex/responses":
		return FormatOpenAIResponses, true
	case "/v1beta/models/*modelAction", "/antigravity/v1beta/models/*modelAction":
		return FormatGemini, true
	}
	return "", false
}

// GeminiModelAction 拆分 Gemini 路由的 *modelAction 参数（如 "/gemini-2.5-pro:streamGenerateContent"）。
// isGenerate 表示该动作是内容生成（generateContent / streamGenerateContent），
// 其余动作（countTokens、embedContent 等）不属于对话。
func GeminiModelAction(param string) (model, action string, isGenerate bool) {
	param = strings.TrimPrefix(strings.TrimSpace(param), "/")
	idx := strings.LastIndex(param, ":")
	if idx < 0 {
		return param, "", false
	}
	model, action = param[:idx], param[idx+1:]
	return model, action, action == "generateContent" || action == "streamGenerateContent"
}

// Key 生成会话分组键：同一用户、同一 API Key 下，系统提示词与第一条用户消息都相同的请求，
// 视为同一场对话的不同轮次。
//
// 网关无法得知客户端的会话 ID，只能用对话开头的内容近似；
// 开头没有用户文本的请求（如只带 previous_response_id 的 Responses 请求）无法归组，
// 此时用 fallback（请求 ID）使其独立成组。
func Key(userID, apiKeyID int64, system, firstUser, fallback string) string {
	h := sha256.New()
	h.Write([]byte("v1|" + strconv.FormatInt(userID, 10) + "|" + strconv.FormatInt(apiKeyID, 10) + "|"))
	h.Write([]byte(system))
	h.Write([]byte{0})
	h.Write([]byte(firstUser))
	if strings.TrimSpace(firstUser) == "" {
		h.Write([]byte{0})
		h.Write([]byte(fallback))
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}
