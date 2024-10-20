/*
log2quickwit v1.5.8

Description:
This program reads log files from eduroam-th.uni.net.th and sends the parsed data to Quickwit for indexing.
It supports ISO8601 timestamp format and traditional date-time formats.

Major changes in v1.5.8:
1. Improved timestamp parsing to handle multiple date-time formats, including date-only formats.
2. Enhanced parseAdditionalFields function to prevent panic from slice bounds out of range.
3. Fixed DestinationIP parsing to store as string instead of net.IP.
4. Improved error handling and logging for parsing errors.

Previous major changes (v1.5.7):
1. Updated timestamp parsing to handle ISO8601 format without timezone (e.g., "2024-10-18T01:53:12").
2. Simplified LogEntry struct to include only necessary fields.
3. Improved error handling for timestamp parsing.
4. Optimized performance for processing log files with consistent ISO8601 timestamp format.

Author: [P.Itarun]
Date: October 20, 2024

Usage:
  ./log2quickwit [flags]

Flags:
  -config string
        Path to the configuration file (default "src2index.properties")
  -logfile string
        Path to the log file to process (overrides the value in config file)
  -quickwit-url string
        URL of the Quickwit server (overrides the value in config file)

Configuration file (src2index.properties) parameters:
  logFilePath    : Path to the log file to process
  quickwitURL    : URL of the Quickwit server
  username       : Username for Quickwit authentication
  password       : Password for Quickwit authentication
  batchSize      : Number of log entries to send in each batch (default 30000)
  maxRetries     : Maximum number of retry attempts for failed requests (default 3)

Note: 
- The program now supports multiple timestamp formats, including ISO8601 and traditional formats.
- Log parsing has been optimized to handle various log entry formats more robustly.
- Improved error handling provides more detailed information for troubleshooting.
- The program will automatically reduce the batch size if it encounters "Payload Too Large" errors from Quickwit.

For more information, please refer to the README.md file.
*/

package main

import (
    "bufio"
    "bytes"
    "encoding/json"
    "fmt"
    "io"
    "log"
    "net/http"
    "os"
    "strconv"
    "strings"
    "time"

    "github.com/fsnotify/fsnotify"
)


type Config struct {
    LogFilePath  string
    QuickwitURL  string
    Username     string
    Password     string
    BatchSize    int
    MaxRetries   int
}

type LogEntry struct {
    Timestamp       string    `json:"timestamp"`
    Hostname        string    `json:"hostname"`
    Process         string    `json:"process"`
    PID             int64     `json:"pid,omitempty"`
    MessageType     string    `json:"message_type"`
    DestinationIP   string    `json:"destination_ip,omitempty"`
    Username        string    `json:"username,omitempty"`
    StationID       string    `json:"station_id,omitempty"`
    Realm           string    `json:"realm,omitempty"`
    ServiceProvider string    `json:"service_provider,omitempty"`
    FullMessage     string    `json:"full_message"`
}

type QuickwitStats struct {
    ValidDocs   int `json:"valid_docs"`
    ErrorDocs   int `json:"error_docs"`
    ParseErrors int `json:"parse_errors"`
}

func main() {
    log.Println("Starting log2quickwit v1.5.7")
    
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
            continue
        }
        log.Printf("Quickwit Indexing Stats for nro-logs:")
        log.Printf("  Valid documents: %d", stats.ValidDocs)
        log.Printf("  Error documents: %d", stats.ErrorDocs)
        log.Printf("  Parse errors: %d", stats.ParseErrors)
    }
}

func parseTimestamp(timestampStr string) (time.Time, error) {
    layouts := []string{
        "2006-01-02T15:04:05",
        "2006-01-02 15:04:05",
        "2006-01-02",
    }

    var timestamp time.Time
    var err error
    for _, layout := range layouts {
        timestamp, err = time.Parse(layout, timestampStr)
        if err == nil {
            return timestamp, nil
        }
    }
    return time.Time{}, fmt.Errorf("unable to parse timestamp: %v", err)
}


func parseLine(line string) (LogEntry, error) {
    entry := LogEntry{
        FullMessage: line,
    }

    parts := strings.Fields(line)
    if len(parts) < 4 {
        return entry, fmt.Errorf("invalid log format: not enough parts")
    }

    // Parse timestamp (ใช้โค้ดเดิมของ v1.5.7)
    timestamp, err := parseTimestamp(parts[0])
    if err != nil {
        return entry, fmt.Errorf("invalid timestamp: %v", err)
    }
    entry.Timestamp = timestamp.Format(time.RFC3339)

    entry.Hostname = parts[1]
    processWithPID := parts[2]

    // Extract process and PID
    pidStart := strings.Index(processWithPID, "[")
    pidEnd := strings.Index(processWithPID, "]")
    if pidStart != -1 && pidEnd != -1 && pidEnd > pidStart {
        entry.Process = processWithPID[:pidStart]
        pidStr := processWithPID[pidStart+1 : pidEnd]
        pid, err := strconv.ParseInt(pidStr, 10, 64)
        if err == nil {
            entry.PID = pid
        }
    } else {
        entry.Process = processWithPID
    }

    // Parse the rest of the message
    if len(parts) > 3 {
        messageContent := strings.Join(parts[3:], " ")
        entry.MessageType = extractMessageType(messageContent)
        parseAdditionalFields(&entry, messageContent)
    }

    return entry, nil
}

// เพิ่มฟังก์ชันใหม่เพื่อแยก message_type
func extractMessageType(message string) string {
    if strings.Contains(message, "Access-Accept") {
        return "Access-Accept"
    } else if strings.Contains(message, "Access-Reject") {
        return "Access-Reject"
    } else if strings.Contains(message, "Access-Challenge") {
        return "Access-Challenge"
    } else if strings.Contains(message, "Accounting-Request") {
        return "Accounting-Request"
    } else if strings.Contains(message, "Accounting-Response") {
        return "Accounting-Response"
    }
    return "Unknown"
}

// เพิ่มฟังก์ชันใหม่เพื่อแยกข้อมูลเพิ่มเติม
func parseAdditionalFields(entry *LogEntry, message string) {
    // แยก message_type (ถ้ายังไม่ได้กำหนดค่า)
    if entry.MessageType == "" {
        entry.MessageType = extractMessageType(message)
    }

    // แยก username
    if userIndex := strings.Index(message, "user "); userIndex != -1 {
        endIndex := strings.IndexAny(message[userIndex+5:], " \n")
        if endIndex != -1 {
            entry.Username = message[userIndex+5 : userIndex+5+endIndex]
        } else {
            entry.Username = message[userIndex+5:]
        }
    }

    // แยก stationid
    if stationIndex := strings.Index(message, "stationid "); stationIndex != -1 {
        endIndex := strings.IndexAny(message[stationIndex+10:], " \n")
        if endIndex != -1 {
            entry.StationID = message[stationIndex+10 : stationIndex+10+endIndex]
        } else {
            entry.StationID = message[stationIndex+10:]
        }
    }

    // แยก realm (from)
    if fromIndex := strings.Index(message, " from "); fromIndex != -1 {
        endIndex := strings.IndexAny(message[fromIndex+6:], " \n")
        if endIndex != -1 {
            entry.Realm = message[fromIndex+6 : fromIndex+6+endIndex]
        } else {
            entry.Realm = message[fromIndex+6:]
        }
    }

    // แยก service_provider (to)
    if toIndex := strings.LastIndex(message, " to "); toIndex != -1 {
        endIndex := strings.IndexAny(message[toIndex+4:], " (\n")
        if endIndex != -1 {
            entry.ServiceProvider = message[toIndex+4 : toIndex+4+endIndex]
        } else {
            entry.ServiceProvider = message[toIndex+4:]
        }
    }

    // แยก destination_ip
    if ipIndex := strings.LastIndex(message, "("); ipIndex != -1 {
        endIndex := strings.LastIndex(message, ")")
        if endIndex != -1 && endIndex > ipIndex {
            entry.DestinationIP = strings.Trim(message[ipIndex+1:endIndex], " ")
        }
    }
}

func parseAccessMessage(entry *LogEntry, message string) {
    parts := strings.Fields(message)
    for i, part := range parts {
        switch {
        case strings.HasPrefix(part, "user"):
            if i+1 < len(parts) {
                entry.Username = parts[i+1]
            }
        case strings.HasPrefix(part, "stationid"):
            if i+1 < len(parts) {
                entry.StationID = parts[i+1]
            }
        case part == "from":
            if i+1 < len(parts) {
                entry.Realm = parts[i+1]
            }
        case part == "to":
            if i+1 < len(parts) {
                entry.ServiceProvider = parts[i+1]
                if i+2 < len(parts) {
                    // แก้ไขส่วนนี้เพื่อกำหนดค่า IP เป็น string โดยตรง
                    entry.DestinationIP = strings.Trim(parts[i+2], "()")
                }
            }
        }
    }
}

func parseMessage(entry *LogEntry, message string) {
    // ... (existing parseMessage function remains unchanged)
}

func sendToQuickwitWithRetry(entries []LogEntry, config Config) error {
    batchSize := len(entries)
    for i := 0; i < config.MaxRetries; i++ {
        err := sendToQuickwit(entries[:batchSize], config)
        if err == nil {
            return nil
        }
        
        log.Printf("Attempt %d failed: %v", i+1, err)
        
        if strings.Contains(err.Error(), "413") || strings.Contains(err.Error(), "Payload Too Large") {
            batchSize = batchSize / 2
            if batchSize < 1 {
                return fmt.Errorf("batch size reduced to zero: %v", err)
            }
            log.Printf("Reducing batch size to %d and retrying", batchSize)
        } else {
            time.Sleep(time.Second * time.Duration(1<<uint(i))) // Exponential backoff
        }
    }
    return fmt.Errorf("failed after %d attempts", config.MaxRetries)
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
    
    // Construct the metrics URL
    metricsURL := strings.TrimSuffix(config.QuickwitURL, "/api/v1/nro-logs/ingest")
    if !strings.HasSuffix(metricsURL, "/") {
        metricsURL += "/"
    }
    metricsURL += "metrics"
    
    req, err := http.NewRequest("GET", metricsURL, nil)
    if err != nil {
        return stats, fmt.Errorf("error creating request: %v", err)
    }
    req.SetBasicAuth(config.Username, config.Password)
    
    resp, err := client.Do(req)
    if err != nil {
        return stats, fmt.Errorf("error sending request: %v", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        body, _ := io.ReadAll(resp.Body)
        return stats, fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(body))
    }

    body, err := io.ReadAll(resp.Body)
    if err != nil {
        return stats, fmt.Errorf("error reading response body: %v", err)
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
        BatchSize:  30000, // Default value
        MaxRetries: 3,     // Default value
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

