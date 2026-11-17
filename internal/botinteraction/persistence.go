package botinteraction

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"fmt"
	"log"
	"strings"
)

// A legacy utf8mb3 table (or a non-UTF8 PostgreSQL database) cannot store emoji. Preserve a clear terminal reply
// rather than reconnecting with an orphaned running request and no explanation.
func (s *Service) saveReply(item domain.BotInteraction, reply, detail string) (string, string, error) {
	err := s.Store.SaveBotReply(item, reply, detail)
	if err == nil {
		return reply, detail, nil
	}
	code := repo.UnstorableTextCode(err)
	if code == "" {
		log.Printf("wecom reply persistence failed id=%s error_type=%T", item.ID, err)
		return reply, detail, err
	}
	log.Printf("wecom reply persistence failed id=%s code=%s", item.ID, code)
	if strings.HasPrefix(code, "MySQL") {
		reply = "AI 回复已生成，但数据库无法保存部分字符。请联系管理员将 Bot 消息表升级为 utf8mb4，然后重新发送请求。"
		detail = fmt.Sprintf("消息保存失败：%s，请升级 bot_interactions 字符集为 utf8mb4", code)
	} else {
		reply = "AI 回复已生成，但数据库无法保存部分字符。请联系管理员把数据库编码改为 UTF8，然后重新发送请求。"
		detail = fmt.Sprintf("消息保存失败：%s，数据库编码不是 UTF8", code)
	}
	err = s.Store.SaveBotReply(item, reply, detail)
	return reply, detail, err
}
