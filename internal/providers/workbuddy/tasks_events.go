package workbuddy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
)

const pathGrowthReport = "/v2/report"

// Only the known growth event shapes are submitted. An acknowledged event is
// never treated as task completion: the runner reads task progress back first.
var growthEventKinds = map[string]string{
	"first_buddy": "chat", "chat_5": "chat", "Model_chat_GLM5.2": "glmchat",
	"create_canvas": "canvas", "template_5": "template", "expert_5": "expert",
	"Expert_team_use_3": "team", "skill_1": "skill", "automation_1": "automation",
	"playbook_prompt": "playbook", "Expert_lighthouse": "lighthouse", "Hp_Appearance": "skin",
}

var growthManualTasks = map[string]string{
	"RichMeow_Chat":               "requires a real conversation in the desktop app",
	"Library_read":                "requires opening and reading the desktop library",
	"Buddy_App":                   "requires entering a Buddy app in the desktop app",
	"Buddy_App_QQ":                "requires entering the QQ teacher app in the desktop app",
	"wb_wechat_oa_subscribe_task": "requires following the official WeChat account for 24 hours",
	"Expert_Philanthropy":         "requires a real donation",
	"black_cat":                   "requires activity on separate days in the night window; automatic night activity is disabled",
}

var growthExpertIDs = []string{"ex_PZw8Gu81HfN4", "ex_ROsDtJbzADFV", "ex_SMUnl0nJbPix", "ex_ZTR062oVBOCW", "ex_a3sSSFBy8qaC", "ex_aG1kvKbq8lPx", "ex_al1vxtUOYQ10", "ex_cZfiyuET9UQP"}
var growthTeamIDs = []string{"CloudOpsTeam", "CloudContentTeam", "CloudDevTeam"}

func buildGrowthEvent(credential Credential, kind string, index int64) (map[string]any, error) {
	if credential.IsGlobal() || credential.EnterpriseID != "" || credential.UID == "" {
		return nil, errors.New("growth events require a personal CN account")
	}
	if index < 0 {
		return nil, errors.New("invalid growth progress")
	}
	id := newCLIRequestID()
	event := map[string]any{"timestamp": time.Now().UnixMilli(), "reportDelay": 0, "userId": credential.UID,
		"conversationId": "wb-task-" + id, "requestId": id, "messageId": id}
	set := func(code string, fields map[string]any) {
		event["eventCode"] = code
		for key, value := range fields {
			event[key] = value
		}
	}
	switch kind {
	case "chat", "glmchat":
		model, name := "deepseek-v4-flash", "DeepSeek V4 Flash"
		if kind == "glmchat" {
			model, name = "glm-5.2", "GLM-5.2"
		}
		set("chat_request_send", map[string]any{"mode": "craft", "inputLength": 12, "requestModelId": model,
			"requestModelName": name, "isPlan": false, "agentName": "default", "agentType": "conversation"})
	case "canvas":
		set("wbx_design_canvas_task_create", map[string]any{"source": "summon_keyword", "isCustomModel": false,
			"name": "", "inputLength": 12, "id": "wbx-canvas-" + id, "cost": 0, "isSuccessful": true})
	case "template":
		set("agent_task_created_with_template", map[string]any{"isCustomModel": true, "id": strconv.FormatInt(index, 10), "name": "幻灯片"})
	case "expert", "team", "lighthouse":
		expertID, expertType := "ex_2cvvUZQhDyeJ", "agent"
		if kind == "expert" {
			expertID = growthExpertIDs[int(index)%len(growthExpertIDs)]
		} else if kind == "team" {
			expertID, expertType = growthTeamIDs[int(index)%len(growthTeamIDs)], "team"
		}
		set("expert_actual_use", map[string]any{"mode": "CLOUD", "id": expertID, "name": expertID, "expertTitle": expertID,
			"type": "02-Engineering", "expertType": expertType, "source": "builtin", "version": "1.0.2", "cost": 0,
			"characterCount": 12, "requestModelId": "deepseek-v4-flash", "requestModelName": "DeepSeek V4 Flash"})
	case "skill":
		set("skill_info", map[string]any{"skillId": "skill_2096525080079265792", "name": "pptx"})
	case "automation":
		set("automated_task_create_suc", map[string]any{"name": "每周工作整理", "type": "cron", "source": "manually",
			"modelId": "deepseek-v4-flash", "modelIsThinking": false,
			"schedule": map[string]any{"type": "recurring", "rrule": "FREQ=WEEKLY;BYDAY=FR;BYHOUR=9;BYMINUTE=0"}, "prompt": "每周五自动整理本周工作"})
	case "playbook":
		set("playbook_prompt_send", map[string]any{"id": "worker-ledger-freedom-dashboard", "name": "打工人小账本",
			"type": "other", "promptLength": 10, "isOfficial": 1, "source": "discover"})
	case "skin":
		set("appearance_skin_apply", map[string]any{"action": "apply", "source": "settings_close", "id": "theme-tkmw7j",
			"vipLevel": "free", "series": "craft", "type": "unknown", "name": "和平精英激战金秋"})
	default:
		return nil, errors.New("unsupported growth event")
	}
	return event, nil
}

func (c *Client) ReportGrowthEvents(ctx context.Context, accountID string, credential Credential, events []map[string]any) error {
	if credential.IsGlobal() || credential.EnterpriseID != "" || credential.UID == "" {
		return errors.New("growth events require a personal CN account")
	}
	body, err := json.Marshal(events)
	if err != nil {
		return err
	}
	_, err = c.growthDo(ctx, accountID, credential, credential.ChatBase(), http.MethodPost, pathGrowthReport, body)
	return err
}
