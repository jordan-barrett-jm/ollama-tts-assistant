package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Data struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
}

var audioQueue = make(chan string, 100) // Audio queue with a buffer
var audioCancelFunc context.CancelFunc
var audioCancelMutex sync.Mutex

func main() {
	// serverUrl := "http://localhost:11434"
	serverUrl := "https://77ufi6a3mul3a3-11434.proxy.runpod.net"
	scanner := bufio.NewScanner(os.Stdin)
	history := []Message{}

	go processAudioQueue() // Start the audio processing goroutine

	for {
		fmt.Print("User (type 'exit' to quit): ")
		if !scanner.Scan() {
			fmt.Println("Error reading input:", scanner.Err())
			return
		}
		userInput := scanner.Text()

		if userInput == "exit" {
			cancelAudioPlayback()
			break
		}

		cancelAudioPlayback() // Cancel current playback and clear queue

		history = append(history, Message{Role: "user", Content: userInput})

		data := Data{
			Model:    "openchat",
			Messages: history,
		}

		jsonData, err := json.Marshal(data)
		if err != nil {
			fmt.Println("Error marshaling data:", err)
			continue
		}

		request, err := http.NewRequest("POST", serverUrl+"/api/chat", bytes.NewBuffer(jsonData))
		if err != nil {
			fmt.Println("Error creating request:", err)
			continue
		}
		request.Header.Set("Content-Type", "application/json")

		client := &http.Client{}
		response, err := client.Do(request)
		if err != nil {
			fmt.Println("Error sending request:", err)
			continue
		}
		defer response.Body.Close()

		if response.StatusCode != 200 {
			fmt.Printf("Received non-200 status code: %d\n", response.StatusCode)
			continue
		}
		fmt.Print("Assistant:")
		var fullResponse, sentenceBuffer string

		responseScanner := bufio.NewScanner(response.Body)
		for responseScanner.Scan() {
			var streamItem struct {
				Model     string `json:"model"`
				CreatedAt string `json:"created_at"`
				Message   struct {
					Role    string        `json:"role"`
					Content string        `json:"content"`
					Images  []interface{} `json:"images"`
				} `json:"message"`
				Done bool `json:"done"`
			}

			if err := json.Unmarshal(responseScanner.Bytes(), &streamItem); err != nil {
				fmt.Println("Error unmarshalling response JSON:", err)
				break
			}

			fmt.Print(streamItem.Message.Content)
			sentenceBuffer += streamItem.Message.Content

			if dotPos := strings.Index(sentenceBuffer, "."); dotPos != -1 {
				audioQueue <- sentenceBuffer[:dotPos+1]

				fullResponse += sentenceBuffer[:dotPos+1]
				sentenceBuffer = sentenceBuffer[dotPos+1:]
			}

			if streamItem.Done {
				if len(sentenceBuffer) > 0 {
					audioQueue <- sentenceBuffer
					fullResponse += sentenceBuffer
				}
				history = append(history, Message{Role: "assistant", Content: fullResponse})
				fmt.Println()
				fmt.Println()
				break
			}
		}
		if err := responseScanner.Err(); err != nil {
			fmt.Println("Error reading streaming response:", err)
		}
	}
}

func cancelAudioPlayback() {
	audioCancelMutex.Lock()
	defer audioCancelMutex.Unlock()

	if audioCancelFunc != nil {
		audioCancelFunc()
	}

	// Drain the audioQueue channel of its current contents.
	// This loop removes items from the queue until it's empty.
	// The 'select' statement is non-blocking; if the channel is empty, it does nothing.
	for {
		select {
		case <-audioQueue:
			// Successfully read from the queue; continue draining.
		default:
			// The queue is empty; exit the loop.
			return
		}
	}
}

func processAudioQueue() {
	for sentence := range audioQueue { // Continuously listen for new sentences
		// Play the audio for the current sentence. This call blocks until the audio finishes playing or is cancelled.
		playAudioFromStreamWithContext(sentence)
	}
}

func playAudioFromStreamWithContext(text string) {
	audioCancelMutex.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	audioCancelFunc = cancel
	audioCancelMutex.Unlock()

	defer cancel() // Ensure the context is cancelled when the function returns

	// Marshal the settings into JSON
	settings := map[string]interface{}{
		"text":     text,
		"language": "EN",
		"speed":    1,
		"speaker":  "EN-BR",
	}
	jsonData, err := json.Marshal(settings)
	if err != nil {
		fmt.Println("Error marshaling settings:", err)
		return
	}

	response, err := http.Post("http://localhost:8888/stream", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		fmt.Println("Error making request to audio stream API:", err)
		return
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		fmt.Printf("Received non-200 status code from audio stream API: %d\n", response.StatusCode)
		return
	}

	cmd := exec.Command("ffplay", "-i", "-", "-nodisp", "-autoexit")
	cmd.Stdin = response.Body
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = io.Discard

	if err := cmd.Start(); err != nil {
		fmt.Println("Error starting ffplay:", err, stderr.String())
		return
	}

	// Use a channel to signal completion of the command.
	doneChan := make(chan error, 1)
	go func() {
		doneChan <- cmd.Wait()
	}()

	select {
	case <-ctx.Done():
		// If the context is cancelled, attempt to kill the process.
		if killErr := cmd.Process.Kill(); killErr != nil {
			fmt.Println("Failed to kill ffplay process:", killErr)
		}
	case err := <-doneChan:
		// Command completed; check for errors.
		if err != nil {
			fmt.Println("ffplay finished with error:", err, stderr.String())
		}
	}
}
