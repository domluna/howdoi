package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/template"
	"time"

	"github.com/gocolly/colly"
	"github.com/spf13/cobra"
)

var models = map[string]string{
	"opus":   "claude-3-opus-20240229",
	"sonnet": "claude-3-sonnet-20240229",
	"haiku":  "claude-3-haiku-20240307",
	"gpt":    "gpt-4o",
}

var modelToProvider = map[string]string{
	"opus":   "anthropic",
	"sonnet": "anthropic",
	"haiku":  "anthropic",
	"gpt":    "openai",
}

type Cost struct {
	// Input is the cost of tokens in the input message
	Input float64
	// Output is the cost of tokens in the output message
	Output float64
}

// Cost per token
var modelCosts = map[string]Cost{
	"claude-3-haiku-20240307":        {Input: 0.25 / 1000000, Output: 1.25 / 1000000},
	"claude-3-sonnet-20240229":       {Input: 3.0 / 1000000, Output: 15.0 / 1000000},
	"claude-3-opus-20240229":         {Input: 15.0 / 1000000, Output: 75.0 / 1000000},
	"gpt-4o":                         {Input: 10.0 / 1000000, Output: 30.0 / 1000000},
	"meta-llama/Llama-3-8b-chat-hf":  {Input: 0.30 / 1000000, Output: 0.30 / 1000000},
	"meta-llama/Llama-3-70b-chat-hf": {Input: 0.9 / 1000000, Output: 0.9 / 1000000},
}

type TextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type Source struct {
	Type      string `json:"type"`
	Data      string `json:"data"`
	MediaType string `json:"media_type"`
}

type ImageContent struct {
	Type   string `json:"type"`
	Source Source `json:"source"`
}

type Message struct {
	Role    string `json:"role"`
	Content []any  `json:"content"`
}

type RequestBody struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	MaxTokens   int       `json:"max_tokens"`
	Temperature float64   `json:"temperature"`
	Stream      bool      `json:"stream"`
}

type ResponseContentText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type Choices struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinsihReason string  `json:"finish_reason"`
}

type ReponseBody struct {
	Choices    []Choices             `json:"choices"`
	Content    []ResponseContentText `json:"content"`
	Role       []string              `json:"role"`
	Type       string                `json:"type"`
	Usage      Usage                 `json:"usage"`
	Model      string                `json:"model"`
	StopReason string                `json:"stop_reason"`
	ID         string                `json:"id"`
}

// Example Response
// {
//   "content": [
//     {
//       "text": "Hi! My name is Claude.",
//       "type": "text"
//     }
//   ],
//   "id": "msg_013Zva2CMHLNnXjNJJKqJ2EF",
//   "model": "claude-3-opus-20240229",
//   "role": "assistant",
//   "stop_reason": "end_turn",
//   "stop_sequence": null,
//   "type": "message",
//   "usage": {
//     "input_tokens": 10,
//     "output_tokens": 25
//   }
// }

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func (u Usage) String() string {
	return fmt.Sprintf("Input Tokens: %d, Output Tokens: %d", u.InputTokens, u.OutputTokens)
}

func calculateCost(model string, usage Usage) float64 {
	cost := modelCosts[model]
	return float64(usage.InputTokens)*cost.Input + float64(usage.OutputTokens)*cost.Output
}

func isFile(str string) bool {
	_, err := os.Stat(str)
	return err == nil
}

func isUrl(str string) bool {
	_, err := url.ParseRequestURI(str)
	return err == nil
}

func scrapeWebPage(url string) (string, error) {
	c := colly.NewCollector()
	var content string

	c.OnHTML("article", func(e *colly.HTMLElement) {
		content = e.Text
	})
	if content == "" {
		c.OnHTML("main", func(e *colly.HTMLElement) {
			content = e.Text
		})
	}

	err := c.Visit(url)
	if err != nil {
		return "", err
	}

	return content, nil
}

func callAPI(model string, r *http.Request) (chan string, error) {
	client := &http.Client{}
	res, err := client.Do(r)
	if err != nil {
		return nil, err
	}

	if res.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(res.Body)
		return nil, errors.New(fmt.Sprintf("API call failed with status code %d, error: %s", res.StatusCode, string(bodyBytes)))
	}

	respChan := make(chan string)
	go func() {
		defer close(respChan)
		defer res.Body.Close()
		var usage Usage

		t1 := time.Now()

		// Read the response body line by line
		buf := bufio.NewReader(res.Body)
		for {
			line, err := buf.ReadString('\n')
			if err != nil {
				if err == io.EOF {
					break
				}
				log.Printf("Error reading response: %v", err)
				break
			}
			if line == "" || line == "\n" {
				continue
			}

			// {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-3.5-turbo-0125", "system_fingerprint": "fp_44709d6fcb", "choices":[{"index":0,"delta":{"content":"Hello"},"logprobs":null,"finish_reason":null}]}
			if strings.HasPrefix(line, "data:") && strings.Contains(line, "gpt") {
				var data struct {
					ID      string `json:"id"`
					Choices []struct {
						FinishReason string `json:"finish_reason"`
						Delta        struct {
							Content string `json:"content"`
						} `json:"delta"`
					} `json:"choices"`
				}
				line = strings.TrimPrefix(line, "data:")
				if err := json.Unmarshal([]byte(line), &data); err == nil {
					if data.Choices[0].FinishReason == "stop" {
						break
					}
					text := data.Choices[0].Delta.Content
					respChan <- text
				}
			} else if strings.HasPrefix(line, "data:") && strings.Contains(line, "content_block_delta") {
				// Check if the line is a content_block_delta event
				var data struct {
					Type  string `json:"type"`
					Delta struct {
						type_ string
						Text  string `json:"text"`
					} `json:"delta"`
				}
				line = strings.TrimPrefix(line, "data:")
				if err := json.Unmarshal([]byte(line), &data); err == nil {
					text := data.Delta.Text
					respChan <- text
				}
			} else if strings.HasPrefix(line, "data:") && strings.Contains(line, "message_delta") {
				// data: {"type": "message_delta", "delta": {"stop_reason": "end_turn", "stop_sequence":null, "usage":{"output_tokens": 15}}}
				line = strings.TrimPrefix(line, "data:")
				var data struct {
					Type  string `json:"type"`
					Usage Usage  `json:"usage"`
				}
				if err := json.Unmarshal([]byte(line), &data); err == nil {
					usage.OutputTokens += data.Usage.OutputTokens
				}
			} else if strings.HasPrefix(line, "data:") && strings.Contains(line, "message_start") {
				// data: {"type": "message_start", "message": {"id": "msg_1nZdL29xx5MUA1yADyHTEsnR8uuvGzszyY", "type": "message", "role": "assistant", "content": [], "model": "claude-3-opus-20240229", "stop_reason": null, "stop_sequence": null, "usage": {"input_tokens": 25, "output_tokens": 1}}}
				line = strings.TrimPrefix(line, "data:")
				var data struct {
					Type    string `json:"type"`
					Message struct {
						Usage Usage `json:"usage"`
					} `json:"message"`
				}
				if err := json.Unmarshal([]byte(line), &data); err == nil {
					usage.InputTokens += data.Message.Usage.InputTokens
				}
			}
		}
		t2 := time.Now()
		time.Sleep(100 * time.Millisecond)
		totalCost := calculateCost(model, usage)
		fmt.Print("\n\n")
		log.Printf("Usage: %s, Total Cost: $%.6f\n", usage, totalCost)
		log.Printf("Tokens per second: %.2f\n", float64(usage.OutputTokens)/t2.Sub(t1).Seconds())
	}()

	return respChan, nil
}

type Document struct {
	Source  string
	Content string
}

var documentTemplate = `
<document>
<source>
{{.Source}}
</source>
<document_content>
{{.Content}}
</document_content>
</document>
`

func isAcceptedImageFile(file string) (string, bool) {
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".gif", ".webp"} {
		if strings.HasSuffix(strings.ToLower(file), ext) {
			if ext == ".jpg" {
				return ".jpeg", true
			}
			return ext, true
		}
	}
	return "", false
}

func main() {
	var model string
	var maxTokens int

	tmpl := template.Must(template.New("documents").Parse(documentTemplate))

	var rootCmd = &cobra.Command{
		Use:   "howdoi [messages...]",
		Short: "CLI tool to interact with the Anthropic API. Messages can be written text or image files.",
		Args:  cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			var url, apiKey, envKey string
			// Check if the model is supported
			_, ok := models[model]
			if !ok {
				log.Println("Error: Unsupported model")
				os.Exit(1)
			}

			provider, _ := modelToProvider[model]

			if provider == "openai" {
				url = "https://api.openai.com/v1/chat/completions"
				envKey = "OPENAI_API_KEY"
			} else if provider == "anthropic" {
				url = "https://api.anthropic.com/v1/messages"
				envKey = "ANTHROPIC_API_KEY"
			} else {
				log.Println("Error: Unsupported provider")
				os.Exit(1)
			}

			apiKey = os.Getenv(envKey)
			if apiKey == "" {
				log.Printf("Error: %s environment variable is not set\n", envKey)
				os.Exit(1)
			}

			// Combine context and user message
			if len(args) <= 0 {
				log.Println("Error: No messages provided")
				os.Exit(1)
			}

			message := Message{Role: "user"}
			for _, a := range args {
				if isFile(a) {
					if ext, ok := isAcceptedImageFile(a); ok {
						imageContent, err := os.ReadFile(a)
						if err != nil {
							log.Println("Error reading image file:", err)
							os.Exit(1)
						}
						base64String := base64.StdEncoding.EncodeToString(imageContent)
						src := Source{Data: base64String, MediaType: "image/" + ext[1:], Type: "base64"}
						message.Content = append(message.Content, ImageContent{Type: "image", Source: src})
					} else {
						fileContent, err := os.ReadFile(a)
						// get the name of the file

						if err != nil {
							log.Println("Error reading context file:", err)
							os.Exit(1)
						}
						d := Document{
							Source:  a,
							Content: string(fileContent),
						}
						var docBuffer bytes.Buffer
						if err := tmpl.Execute(&docBuffer, d); err != nil {
							log.Println("Error rendering the template:", err)
							os.Exit(1)
						}
						message.Content = append(message.Content, TextContent{Type: "text", Text: docBuffer.String()})

					}
				} else if isUrl(a) {
					log.Printf("Scraping the web page: %s\n", a)
					fileContent, err := scrapeWebPage(a)
					if err != nil {
						log.Println("Error scraping the web page:", err)
						os.Exit(1)
					}
					d := Document{
						Source:  a,
						Content: string(fileContent),
					}
					var docBuffer bytes.Buffer
					if err := tmpl.Execute(&docBuffer, d); err != nil {
						log.Println("Error rendering the template:", err)
						os.Exit(1)
					}
					message.Content = append(message.Content, TextContent{Type: "text", Text: docBuffer.String()})
				} else {
					message.Content = append(message.Content, TextContent{Type: "text", Text: a})
				}
			}

			rq := RequestBody{
				Model:       models[model],
				Messages:    []Message{message},
				MaxTokens:   maxTokens,
				Temperature: 0.0,
				Stream:      true,
			}

			// Create a HTTP post request
			jsonBody, err := json.Marshal(rq)
			if err != nil {
				log.Println("Error marshalling the request body:", err)
				os.Exit(1)
			}

			r, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonBody))
			if err != nil {
				log.Println("Error creating the request:", err)
				os.Exit(1)
			}

			r.Header.Add("content-type", "application/json")
			if provider == "openai" {
				// add authorization header
				r.Header.Add("Authorization", "Bearer "+apiKey)
			} else if provider == "anthropic" {
				r.Header.Add("x-api-key", apiKey)
				r.Header.Add("anthropic-version", "2023-06-01")
			}

			log.Println("Calling the API ... ", url, models[model])

			respChan, err := callAPI(models[model], r)
			if err != nil {
				log.Println("Error calling the API:", err)
				os.Exit(1)
			}
			for text := range respChan {
				fmt.Print(text)
			}
			if provider == "openai" {
				log.Println("NOTE: OpenAI doesn't provide usage metrics in streaming mode !!!")
			}

		},
	}

	rootCmd.Flags().StringVarP(&model, "model", "m", "haiku", "Model to use)")
	rootCmd.Flags().IntVarP(&maxTokens, "max-tokens", "t", 1000, "Maximum number of tokens to generate")

	if err := rootCmd.Execute(); err != nil {
		log.Println(err)
		os.Exit(1)
	}
}
