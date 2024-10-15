/*
log2quickwit v1.1.0

Description:
This program reads log files and sends the data to Quickwit for indexing.

Major changes from v1.0.0:
1. Implemented inotify for efficient file change detection
2. Improved log processing to only index new data
3. Enhanced error handling and logging
4. Added program header with version information
5. Implemented more robust config loading

Author: [P.Itarun]
Date: [15 Oct 2024]
*/

package main

import (
    "bufio"
    "bytes"
    "encoding/json"
    "fmt"
    "io"
    "log"
    "net"
    "net/http"
    "os"
    "strconv"
    "strings"
    "time"

    "github.com/fsnotify/fsnotify"
)

type Config struct {
    LogFilePath string
    QuickwitURL string
    Username    string
    Password    string
    BatchSize   int
    MaxRetries  int
}

type LogEntry struct {
    Timestamp       string `json:"timestamp"`
    Hostname        string `json:"hostname"`
    Process         string `json:"process"`
    PID             int64  `json:"pid"`
    LogLevel        string `json:"log_level,omitempty"`
    MessageType     string `json:"message_type"`
    FullMessage     string `json:"full_message"`
    SourceIP        net.IP `json:"source_ip,omitempty"`
    DestinationIP   net.IP `json:"destination_ip,omitempty"`
    Username        string `json:"username,omitempty"`
    StationID       string `json:"station_id,omitempty"`
    Status          string `json:"status,omitempty"`
    Realm           string `json:"realm,omitempty"`
    ServiceProvider string `json:"service_provider,omitempty"`
    ErrorMessage    string `json:"error_message,omitempty"`
    RequestID       int64  `json:"request_id,omitempty"`
    UDPPeer         net.IP `json:"udp_peer,omitempty"`
    Action          string `json:"action,omitempty"`
}

type QuickwitStats struct {
    ValidDocs   int `json:"valid_docs"`
    ErrorDocs   int `json:"error_docs"`
    ParseErrors int `json:"parse_errors"`
}

func main() {
    log.Println("Starting log2quickwit v1.1.0")
    
    config, err := loadConfig("src2index.properties")
    if err != nil {
        log.Fatalf("Error loading configuration: %v", err)
    }

    go showStats(config)

    if err := processLogFile(config); err != nil {
        log.Fatalf("Error processing log file: %v", err)
    }
}

func processLogFile(config Config) error {
    watcher, err := fsnotify.NewWatcher()
    if err != nil {
        return fmt.Errorf("error creating watcher: %v", err)
    }
    defer watcher.Close()

    file, err := os.Open(config.LogFilePath)
    if err != nil {
        return fmt.Errorf("error opening file: %v", err)
    }
    defer file.Close()

    var lastPosition int64
    if err := processExistingData(file, &lastPosition, config); err != nil {
        return fmt.Errorf("error processing existing data: %v", err)
    }

    err = watcher.Add(config.LogFilePath)
    if err != nil {
        return fmt.Errorf("error adding file to watcher: %v", err)
    }

    log.Println("Watching for file changes...")
    for {
        select {
        case event, ok := <-watcher.Events:
            if !ok {
                return nil
            }
            if event.Op&fsnotify.Write == fsnotify.Write {
                if err := processNewData(file, &lastPosition, config); err != nil {
                    log.Printf("Error processing new data: %v", err)
                }
            }
        case err, ok := <-watcher.Errors:
            if !ok {
                return nil
            }
            log.Printf("Error watching file: %v", err)
        }
    }
}

func processExistingData(file *os.File, lastPosition *int64, config Config) error {
    log.Println("Processing existing data...")
    scanner := bufio.NewScanner(file)
    var entries []LogEntry
    lineCount := 0
    errorCount := 0

    for scanner.Scan() {
        lineCount++
        line := scanner.Text()
        entry, err := parseLine(line)
        if err != nil {
            log.Printf("Error parsing line %d: %v\nLine content: %s", lineCount, err, line)
            errorCount++
            continue
        }

        entries = append(entries, entry)

        if len(entries) >= config.BatchSize {
            if err := sendToQuickwitWithRetry(entries, config); err != nil {
                log.Printf("Error sending batch to Quickwit: %v", err)
            }
            entries = []LogEntry{}
        }
    }

    if len(entries) > 0 {
        if err := sendToQuickwitWithRetry(entries, config); err != nil {
            log.Printf("Error sending final batch to Quickwit: %v", err)
        }
    }

    *lastPosition, _ = file.Seek(0, io.SeekCurrent)
    log.Printf("Finished processing existing log data. Total lines: %d, Errors: %d", lineCount, errorCount)
    return nil
}

func processNewData(file *os.File, lastPosition *int64, config Config) error {
    newEntries, err := readNewEntries(file, lastPosition)
    if err != nil {
        return fmt.Errorf("error reading new entries: %v", err)
    }

    if len(newEntries) > 0 {
        if err := sendToQuickwitWithRetry(newEntries, config); err != nil {
            return fmt.Errorf("error sending new entries to Quickwit: %v", err)
        }
        log.Printf("Successfully sent %d new entries to Quickwit", len(newEntries))
    }

    return nil
}

func readNewEntries(file *os.File, lastPosition *int64) ([]LogEntry, error) {
    _, err := file.Seek(*lastPosition, io.SeekStart)
    if err != nil {
        return nil, fmt.Errorf("error seeking file: %v", err)
    }

    scanner := bufio.NewScanner(file)
    var newEntries []LogEntry

    for scanner.Scan() {
        line := scanner.Text()
        entry, err := parseLine(line)
        if err != nil {
            log.Printf("Error parsing line: %v\nLine content: %s", err, line)
            continue
        }
        newEntries = append(newEntries, entry)
    }

    if err := scanner.Err(); err != nil {
        return nil, fmt.Errorf("error scanning file: %v", err)
    }

    *lastPosition, _ = file.Seek(0, io.SeekCurrent)
    return newEntries, nil
}

func showStats(config Config) {
    ticker := time.NewTicker(time.Minute)
    defer ticker.Stop()

    for range ticker.C {
        stats, err := getQuickwitIndexingStats(config)
        if err != nil {
            log.Printf("Error getting Quickwit indexing stats: %v", err)
        } else {
            log.Printf("Quickwit Indexing Stats for nro-logs:")
            log.Printf("  Valid documents: %d", stats.ValidDocs)
            log.Printf("  Error documents: %d", stats.ErrorDocs)
            log.Printf("  Parse errors: %d", stats.ParseErrors)
        }
    }
}

func parseLine(line string) (LogEntry, error) {
    entry := LogEntry{
        FullMessage: line,
    }

    parts := strings.SplitN(line, " ", 3)
    if len(parts) < 3 {
        return entry, fmt.Errorf("invalid log format: not enough parts")
    }

    timestamp, err := time.Parse("2006-01-02T15:04:05", parts[0])
    if err != nil {
        return entry, fmt.Errorf("invalid timestamp format: %v", err)
    }
    entry.Timestamp = timestamp.Format(time.RFC3339)

    remaining := parts[2]

    if strings.HasPrefix(remaining, "last message repeated") {
        entry.Process = "system"
        entry.MessageType = "repeat"
        return entry, nil
    }

    hostAndRest := strings.SplitN(remaining, " ", 2)
    if len(hostAndRest) < 2 {
        return entry, fmt.Errorf("invalid log format: can't separate hostname")
    }

    entry.Hostname = hostAndRest[0]
    processAndMessage := hostAndRest[1]

    pidStart := strings.Index(processAndMessage, "[")
    pidEnd := strings.Index(processAndMessage, "]:")
    if pidStart != -1 && pidEnd != -1 && pidEnd > pidStart {
        entry.Process = strings.TrimSpace(processAndMessage[:pidStart])
        pidStr := processAndMessage[pidStart+1 : pidEnd]
        pid, err := strconv.ParseInt(pidStr, 10, 64)
        if err == nil {
            entry.PID = pid
        }
        message := strings.TrimSpace(processAndMessage[pidEnd+2:])
        parseMessage(&entry, message)
    } else {
        colonIndex := strings.Index(processAndMessage, ":")
        if colonIndex != -1 {
            entry.Process = strings.TrimSpace(processAndMessage[:colonIndex])
            message := strings.TrimSpace(processAndMessage[colonIndex+1:])
            parseMessage(&entry, message)
        } else {
            entry.Process = "unknown"
            parseMessage(&entry, processAndMessage)
        }
    }

    return entry, nil
}

func parseMessage(entry *LogEntry, message string) {
    // ... (existing parseMessage function remains unchanged)
}

func sendToQuickwitWithRetry(entries []LogEntry, config Config) error {
    var err error
    for i := 0; i < config.MaxRetries; i++ {
        err = sendToQuickwit(entries, config)
        if err == nil {
            return nil
        }
        log.Printf("Attempt %d failed: %v. Retrying...", i+1, err)
        time.Sleep(time.Second * time.Duration(1<<uint(i))) // Exponential backoff
    }
    return fmt.Errorf("failed after %d attempts: %v", config.MaxRetries, err)
}

func sendToQuickwit(entries []LogEntry, config Config) error {
    var buffer bytes.Buffer
    for _, entry := range entries {
        jsonData, err := json.Marshal(entry)
        if err != nil {
            log.Printf("Error marshaling entry: %v", err)
            continue
        }
        buffer.Write(jsonData)
        buffer.WriteString("\n")
    }

    req, err := http.NewRequest("POST", config.QuickwitURL, &buffer)
    if err != nil {
        return fmt.Errorf("error creating request: %v", err)
    }

    req.SetBasicAuth(config.Username, config.Password)
    req.Header.Set("Content-Type", "application/json")

    client := &http.Client{Timeout: 30 * time.Second}
    resp, err := client.Do(req)
    if err != nil {
        return fmt.Errorf("error sending request: %v", err)
    }
    defer resp.Body.Close()

    body, _ := io.ReadAll(resp.Body)
    if resp.StatusCode != http.StatusOK {
        return fmt.Errorf("error response: Status %d, Body: %s", resp.StatusCode, string(body))
    }

    log.Printf("Successfully sent %d entries. Response: %s", len(entries), string(body))
    return nil
}

func getQuickwitIndexingStats(config Config) (QuickwitStats, error) {
    var stats QuickwitStats
    client := &http.Client{Timeout: 10 * time.Second}
    metricsURL := strings.TrimSuffix(config.QuickwitURL, "/api/v1/nro-logs/ingest") + "/metrics"
    req, err := http.NewRequest("GET", metricsURL, nil)
    if err != nil {
        return stats, err
    }
    req.SetBasicAuth(config.Username, config.Password)
    
    resp, err := client.Do(req)
    if err != nil {
        return stats, err
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return stats, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
    }

    body, err := io.ReadAll(resp.Body)
    if err != nil {
        return stats, err
    }

    // Parse metrics
    lines := strings.Split(string(body), "\n")
    for _, line := range lines {
        if strings.Contains(line, "quickwit_indexing_processed_docs_total") && strings.Contains(line, `index="nro-logs"`) {
            parts := strings.Fields(line)
            if len(parts) == 2 {
                value, err := strconv.ParseInt(parts[1], 10, 64)
                if err == nil {
                    if strings.Contains(line, `docs_processed_status="valid"`) {
                        stats.ValidDocs = int(value)
                    } else if strings.Contains(line, `docs_processed_status="doc_mapper_error"`) {
                        stats.ErrorDocs = int(value)
                    } else if strings.Contains(line, `docs_processed_status="json_parse_error"`) {
                        stats.ParseErrors = int(value)
                    }
                }
            }
        }
    }

    return stats, nil
}

func loadConfig(filename string) (Config, error) {
    config := Config{
        BatchSize:  1000, // Default value
        MaxRetries: 3,    // Default value
    }

    file, err := os.Open(filename)
    if err != nil {
        return config, err
    }
    defer file.Close()

    scanner := bufio.NewScanner(file)
    for scanner.Scan() {
        line := strings.TrimSpace(scanner.Text())
        if line == "" || strings.HasPrefix(line, "#") {
            continue
        }

        parts := strings.SplitN(line, "=", 2)
        if len(parts) != 2 {
            continue
        }

        key := strings.TrimSpace(parts[0])
        value := strings.TrimSpace(parts[1])
        value = strings.Trim(value, "\"") // Remove quotes if present

        switch key {
        case "logFilePath":
            config.LogFilePath = value
        case "quickwitURL":
            config.QuickwitURL = value
        case "username":
            config.Username = value
        case "password":
            config.Password = value
        case "batchSize":
            if i, err := strconv.Atoi(value); err == nil {
                config.BatchSize = i
            }
        case "maxRetries":
            if i, err := strconv.Atoi(value); err == nil {
                config.MaxRetries = i
            }
        }
    }

    if err := scanner.Err(); err != nil {
        return config, err
    }

    // Validate required fields
    if config.LogFilePath == "" || config.QuickwitURL == "" || config.Username == "" || config.Password == "" {
        return config, fmt.Errorf("missing required configuration")
    }

    return config, nil
}