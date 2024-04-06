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
}

type Cost struct {
	// Input is the cost of tokens in the input message
	Input float64
	// Output is the cost of tokens in the output message
	Output float64
}

// Cost per token
var modelCosts = map[string]Cost{

	"claude-3-haiku-20240307":  {Input: 0.25 / 1000000, Output: 1.25 / 1000000},
	"claude-3-sonnet-20240229": {Input: 3.0 / 1000000, Output: 15.0 / 1000000},
	"claude-3-opus-20240229":   {Input: 15.0 / 1000000, Output: 75.0 / 1000000},
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

type ReponseBody struct {
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

func callAPI(url string, apiKey string, body RequestBody) (string, error) {
	// Create a HTTP post request
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	// log.Println(string(jsonBody))
	r, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", err
	}
	r.Header.Add("content-type", "application/json")
	r.Header.Add("x-api-key", apiKey)
	r.Header.Add("anthropic-version", "2023-06-01")

	client := &http.Client{}
	res, err := client.Do(r)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		// write body
		bodyBytes, _ := io.ReadAll(res.Body)
		return "", errors.New(fmt.Sprintf("API call failed with status code %d, error: %s", res.StatusCode, string(bodyBytes)))
	}

	var responseBody ReponseBody
	json.NewDecoder(res.Body).Decode(&responseBody)

	totalCost := calculateCost(body.Model, responseBody.Usage)
	log.Println("Usage:", responseBody.Usage)
	log.Printf("Total Cost: $%.6f\n", totalCost)

	if len(responseBody.Content) <= 0 {
		return "", errors.New("No response content found")
	}

	return responseBody.Content[0].Text, nil
}

func callAPIStreaming(url string, apiKey string, body RequestBody) (chan string, error) {
	// Create a HTTP post request
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	r, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, err
	}
	r.Header.Add("content-type", "application/json")
	r.Header.Add("x-api-key", apiKey)
	r.Header.Add("anthropic-version", "2023-06-01")
	r.Header.Add("anthropic-beta", "messages-2023-12-15")

	client := &http.Client{}
	res, err := client.Do(r)
	if err != nil {
		return nil, err
	}

	if res.StatusCode != http.StatusOK {
		// write body
		bodyBytes, _ := io.ReadAll(res.Body)
		return nil, errors.New(fmt.Sprintf("API call failed with status code %d, error: %s", res.StatusCode, string(bodyBytes)))
	}

	// Set up the streaming response channel
	respChan := make(chan string)

	// Handle the streaming response
	go func() {
		defer close(respChan)
		defer res.Body.Close()
		model := body.Model
		var usage Usage

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

			// Check if the line is a content_block_delta event
			if strings.HasPrefix(line, "data:") && strings.Contains(line, "content_block_delta") {
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
		time.Sleep(100 * time.Millisecond)
		totalCost := calculateCost(model, usage)
		fmt.Print("\n\n")
		log.Printf("Usage: %s, Total Cost: $%.6f\n", usage, totalCost)
	}()

	return respChan, nil
}

type Document struct {
	Index   int
	Source  string
	Content string
}

var documentTemplate = `
<documents>
{{- range .}}
<document index="{{.Index}}">
<source>
{{.Source}}
</source>
<document_content>
{{.Content}}
</document_content>
</document>
{{- end}}
</documents>`

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
	var stream bool
	var contextFiles []string

	apiURL := "https://api.anthropic.com/v1/messages"
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		log.Println("Error: ANTHROPIC_API_KEY environment variable is not set")
		os.Exit(1)
	}

	var rootCmd = &cobra.Command{
		Use:   "howdoi [messages...]",
		Short: "CLI tool to interact with the Anthropic API. Messages can be written text or image files.",
		Args:  cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			var documents []struct {
				Index   int
				Source  string
				Content string
			}
			var contextContent string

			log.Println("Context Files:", contextFiles)

			// Combine context and user message
			if len(args) <= 0 {
				log.Println("Error: No messages provided")
				os.Exit(1)
			}

			if len(contextFiles) > 0 {
				contextContent = "Here are some documents for you to reference for your task:\n\n"
			}
			for i, file := range contextFiles {
				if isFile(file) {
					fileContent, err := os.ReadFile(file)
					// get the name of the file

					if err != nil {
						log.Println("Error reading context file:", err)
						os.Exit(1)
					}
					documents = append(documents, Document{
						Index:   i + 1,
						Source:  file,
						Content: string(fileContent),
					})
				} else if isUrl(file) {
					log.Printf("Scraping the web page: %s\n", file)
					fileContent, err := scrapeWebPage(file)
					if err != nil {
						log.Println("Error scraping the web page:", err)
						os.Exit(1)
					}
					documents = append(documents, Document{
						Index:   i + 1,
						Source:  file,
						Content: fileContent,
					})
				} else {
					log.Printf("Error: Unsupported context file type, skipping %s.\n", file)
				}
			}
			if len(documents) > 0 {
				// Render the template
				var docBuffer bytes.Buffer
				tmpl := template.Must(template.New("documents").Parse(documentTemplate))
				if err := tmpl.Execute(&docBuffer, documents); err != nil {
					log.Println("Error rendering the template:", err)
					os.Exit(1)
				}
				contextContent += docBuffer.String()
			}

			message := Message{Role: "user"}
			for _, a := range args {
				if ext, ok := isAcceptedImageFile(a); ok {
					imageContent, err := os.ReadFile(a)
					if err != nil {
						log.Println("Error reading image file:", err)
						os.Exit(1)
					}
					base64String := base64.StdEncoding.EncodeToString(imageContent)
					src := Source{Data: base64String, MediaType: "image/" + ext[1:], Type: "base64"}
					message.Content = append(message.Content, ImageContent{Type: "image", Source: src})
				} else if _, err := os.Stat(a); !os.IsNotExist(err) {
					log.Println("File type not supported, skipping:", a)
					continue
				} else {
					message.Content = append(message.Content, TextContent{Type: "text", Text: a})
				}
			}

			// Check if the model is supported
			_, ok := models[model]
			if !ok {
				log.Println("Error: Unsupported model")
				os.Exit(1)
			}

			rq := RequestBody{
				Model:       models[model],
				Messages:    []Message{message},
				MaxTokens:   maxTokens,
				Temperature: 0.0,
				Stream:      stream,
			}

			if stream {
				// Call the API with streaming
				respChan, err := callAPIStreaming(apiURL, apiKey, rq)
				if err != nil {
					log.Println("Error calling the API:", err)
					os.Exit(1)
				}
				for text := range respChan {
					fmt.Print(text)
				}
			} else {
				response, err := callAPI(apiURL, apiKey, rq)
				if err != nil {
					log.Println("Error calling the API:", err)
					os.Exit(1)
				}
				fmt.Println(response)
			}

		},
	}

	rootCmd.Flags().StringVarP(&model, "model", "m", "haiku", "Model to use)")
	rootCmd.Flags().IntVarP(&maxTokens, "max-tokens", "t", 1000, "Maximum number of tokens to generate")
	rootCmd.Flags().StringSliceVarP(&contextFiles, "context", "c", []string{}, "Context files to use")
	rootCmd.Flags().BoolVarP(&stream, "stream", "s", true, "Stream the response")

	if err := rootCmd.Execute(); err != nil {
		log.Println(err)
		os.Exit(1)
	}
}
