package controller

import (
	"bufio"
	"encoding/json"
	"fmt"
	"genspark2api/common"
	"genspark2api/common/config"
	"genspark2api/model"
	"github.com/Danny-Dasilva/CycleTLS/cycletls"
	"github.com/gin-gonic/gin"
	"io"
	"strings"
	"time"
)

const (
	baseURL          = "https://www.genspark.ai"
	apiEndpoint      = baseURL + "/api/copilot/ask"
	chatType         = "COPILOT_MOA_CHAT"
	responseIDFormat = "chatcmpl-%s"
)

// createRequestBody 创建请求体
func createRequestBody(openAIReq *model.OpenAIChatCompletionRequest) map[string]interface{} {
	return map[string]interface{}{
		"type":                 chatType,
		"current_query_string": "type=" + chatType,
		"messages":             openAIReq.Messages,
		"user_s_input":         openAIReq.Messages[len(openAIReq.Messages)-1].Content,
		"action_params":        map[string]interface{}{},
		"extra_data": map[string]interface{}{
			"models":                 []string{openAIReq.Model},
			"run_with_another_model": false,
			"writingContent":         nil,
		},
	}
}

// createStreamResponse 创建流式响应
func createStreamResponse(responseId, modelName string, delta model.OpenAIDelta, finishReason string) model.OpenAIChatCompletionResponse {
	return model.OpenAIChatCompletionResponse{
		ID:      responseId,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   modelName,
		Choices: []model.OpenAIChoice{
			{
				Index:        0,
				Delta:        delta,
				FinishReason: finishReason,
			},
		},
	}
}

// handleStreamResponse 处理流式响应
func handleStreamResponse(c *gin.Context, reader *bufio.Reader, responseId, model string) bool {
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return false
		}

		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		var event map[string]interface{}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		eventType, ok := event["type"].(string)
		if !ok {
			continue
		}

		switch eventType {
		case "message_field_delta":
			if err := handleMessageFieldDelta(c, event, responseId, model); err != nil {
				return false
			}
		case "message_result":
			return handleMessageResult(c, responseId, model)
		}
	}
	return false
}

// handleMessageFieldDelta 处理消息字段增量
func handleMessageFieldDelta(c *gin.Context, event map[string]interface{}, responseId, modelName string) error {
	fieldName, ok := event["field_name"].(string)
	if !ok || fieldName != "session_state.answer" {
		return nil
	}

	delta, ok := event["delta"].(string)
	if !ok {
		return nil
	}

	streamResp := createStreamResponse(responseId, modelName, model.OpenAIDelta{Content: delta}, "")
	return sendSSEvent(c, streamResp)
}

// handleMessageResult 处理消息结果
func handleMessageResult(c *gin.Context, responseId, modelName string) bool {
	streamResp := createStreamResponse(responseId, modelName, model.OpenAIDelta{}, "stop")
	if err := sendSSEvent(c, streamResp); err != nil {
		return false
	}
	c.SSEvent("", "[DONE]")
	return false
}

// sendSSEvent 发送SSE事件
func sendSSEvent(c *gin.Context, response model.OpenAIChatCompletionResponse) error {
	jsonResp, err := json.Marshal(response)
	if err != nil {
		return err
	}
	c.SSEvent("", string(jsonResp))
	c.Writer.Flush()
	return nil
}

// makeRequest 发送HTTP请求
func makeRequest(client cycletls.CycleTLS, jsonData []byte, isStream bool) (cycletls.Response, error) {
	cookie, err := common.RandomElement(config.GSCookies)
	if err != nil {
		return cycletls.Response{}, err
	}

	accept := "application/json"
	if isStream {
		accept = "text/event-stream"
	}

	return client.Do(apiEndpoint, cycletls.Options{
		Timeout: 10 * 60 * 60,
		Body:    string(jsonData),
		Method:  "POST",
		Headers: map[string]string{
			"Content-Type": "application/json",
			"Accept":       accept,
			"Origin":       baseURL,
			"Referer":      baseURL + "/",
			"Cookie":       cookie,
		},
	}, "POST")
}

// ChatForOpenAI 处理OpenAI聊天请求
func ChatForOpenAI(c *gin.Context) {
	var openAIReq model.OpenAIChatCompletionRequest
	if err := c.BindJSON(&openAIReq); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	requestBody := createRequestBody(&openAIReq)
	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		c.JSON(500, gin.H{"error": "Failed to marshal request body"})
		return
	}

	client := cycletls.Init()

	if openAIReq.Stream {
		handleStreamRequest(c, client, jsonData, openAIReq.Model)
	} else {
		handleNonStreamRequest(c, client, jsonData, openAIReq.Model)
	}
}

// handleStreamRequest 处理流式请求
func handleStreamRequest(c *gin.Context, client cycletls.CycleTLS, jsonData []byte, model string) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	responseId := fmt.Sprintf(responseIDFormat, time.Now().Format("20060102150405"))

	c.Stream(func(w io.Writer) bool {
		response, err := makeRequest(client, jsonData, true)
		if err != nil {
			return false
		}

		reader := bufio.NewReader(strings.NewReader(response.Body))
		return handleStreamResponse(c, reader, responseId, model)
	})
}

// handleNonStreamRequest 处理非流式请求
// handleNonStreamRequest 处理非流式请求
func handleNonStreamRequest(c *gin.Context, client cycletls.CycleTLS, jsonData []byte, modelName string) {
	response, err := makeRequest(client, jsonData, false)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	reader := strings.NewReader(response.Body)
	scanner := bufio.NewScanner(reader)

	var content string
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Println(line)
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			var parsedResponse struct {
				Type      string `json:"type"`
				FieldName string `json:"field_name"`
				Content   string `json:"content"`
			}
			if err := json.Unmarshal([]byte(data), &parsedResponse); err != nil {
				continue
			}
			if parsedResponse.Type == "message_result" {
				content = parsedResponse.Content
				break
			}
		}
	}

	if content == "" {
		c.JSON(500, gin.H{"error": "No valid response content"})
		return
	}

	// 创建并返回 OpenAIChatCompletionResponse 结构
	resp := model.OpenAIChatCompletionResponse{
		ID:      fmt.Sprintf(responseIDFormat, time.Now().Format("20060102150405")),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   modelName,
		Choices: []model.OpenAIChoice{
			{
				Message: model.OpenAIMessage{
					Role:    "assistant",
					Content: content,
				},
				FinishReason: "stop",
			},
		},
	}

	c.JSON(200, resp)
}
