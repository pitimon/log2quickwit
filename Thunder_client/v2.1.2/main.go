/*
Program: eduroam-accept (User Accept Roaming)
Version: 2.1.2
Description: This program aggregates Access-Accept events for users from a specified domain
             using the Quickwit search engine. It collects data over a specified time range,
             processes the results, and outputs the aggregated data to a JSON file.

Usage: ./eduroam-accept <domain> [days|DD-MM-YYYY]
  <domain>: The domain to search for (e.g., 'example.ac.th' or 'etlr1' or 'etlr2')
  [days]: Optional. The number of days to look back from the current date. Default is 1. Max is 366.
  [DD-MM-YYYY]: Optional. A specific date to process data for.

Features:
- Concurrent querying and processing using goroutines for improved performance
- Flexible time range specification: number of days or specific date
- Aggregation of user access accept events
- Output of results in JSON format with timing information
- Simplified output structure for easier consumption
- Progress reporting during data processing

Changes in version 2.1.2:
- Added support for specifying a single date in DD-MM-YYYY format
- Improved date parsing and validation
- Updated progress reporting to handle both day range and specific date cases
- Modified output file naming convention for specific date queries
- Enhanced error handling for invalid date inputs

Changes in version 2.1.1:
- Implemented worker pool for improved performance
- Added progress reporting
- Improved documentation
- Refactored code structure

Changes in version 2.1.0:
- Changed output format to a simplified structure
- Improved comments and documentation
- Added support for 'etlr2' domain
- Modified getDomain function to handle different domain formats
- Added summary information in the output

Changes in version 2.0.0:
- Changed query from "Access-Reject" to "Access-Accept"
- Updated result processing to count days of activity instead of event occurrences
- Added service provider information to the output
- Restructured output to show data by username and service provider 
- Improved error handling and logging

Author: [P.Itarun]
Date: [21 Oct 2024]
License: [License Information if applicable]
*/

package main

import (
    "bufio"
    "encoding/json"
    "fmt"
    "io"
    "log"
    "net/http"
    "os"
    "sort"
    "strconv"
    "strings"
    "sync"
    "time"
    "sync/atomic"
)

// Properties represents the authentication properties for Quickwit API
type Properties struct {
    QWUser string
    QWPass string
    QWURL  string
}

// LogEntry represents a single log entry from Quickwit search results
type LogEntry struct {
    Username        string    `json:"username"`
    ServiceProvider string    `json:"service_provider"`
    Timestamp       time.Time `json:"timestamp"`
}

// UserStats contains statistics for a user
type UserStats struct {
    DaysActive int
    Providers  map[string]bool
}

// ProviderStats contains statistics for a service provider
type ProviderStats struct {
    Users map[string]bool
}

// Result holds the aggregated results
type Result struct {
    Users     map[string]*UserStats
    Providers map[string]*ProviderStats
}

// SimplifiedOutputData represents a simplified structure of the output JSON file
type SimplifiedOutputData struct {
    QueryInfo struct {
        Domain    string `json:"domain"`
        Days      int    `json:"days"`
        StartDate string `json:"start_date"`
        EndDate   string `json:"end_date"`
    } `json:"query_info"`
    Description   string `json:"description"`
    Summary       struct {
        TotalUsers     int `json:"total_users"`
        TotalProviders int `json:"total_providers"`
    } `json:"summary"`
    ProviderStats []struct {
        Provider  string   `json:"provider"`
        UserCount int      `json:"user_count"`
        Users     []string `json:"users"`
    } `json:"provider_stats"`
    UserStats []struct {
        Username   string   `json:"username"`
        DaysActive int      `json:"days_active"`
        Providers  []string `json:"providers"`
    } `json:"user_stats"`
}

// Job represents a single day's query job
type Job struct {
    StartTimestamp int64
    EndTimestamp   int64
}

// readProperties reads the authentication properties from a file
func readProperties(filePath string) (Properties, error) {
    file, err := os.Open(filePath)
    if err != nil {
        return Properties{}, err
    }
    defer file.Close()

    props := Properties{}
    scanner := bufio.NewScanner(file)
    for scanner.Scan() {
        line := scanner.Text()
        if line != "" && !strings.HasPrefix(line, "#") {
            parts := strings.SplitN(line, "=", 2)
            if len(parts) == 2 {
                key := strings.TrimSpace(parts[0])
                value := strings.TrimSpace(parts[1])
                switch key {
                case "QW_USER":
                    props.QWUser = value
                case "QW_PASS":
                    props.QWPass = value
                case "QW_URL":
                    props.QWURL = strings.TrimPrefix(value, "=")
                }
            }
        }
    }
    return props, scanner.Err()
}

// getQuickwitResults retrieves search results from Quickwit API
func getQuickwitResults(query map[string]interface{}, auth Properties, resultChan chan<- LogEntry) (int64, error) {
    client := &http.Client{}
    jsonQuery, _ := json.Marshal(query)
    req, err := http.NewRequest("POST", auth.QWURL+"/api/v1/nro-logs/search", strings.NewReader(string(jsonQuery)))
    if err != nil {
        return 0, fmt.Errorf("error creating request: %v", err)
    }

    req.SetBasicAuth(auth.QWUser, auth.QWPass)
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("Accept", "application/json")

    resp, err := client.Do(req)
    if err != nil {
        return 0, fmt.Errorf("error sending request: %v", err)
    }
    defer resp.Body.Close()

    bodyBytes, err := io.ReadAll(resp.Body)
    if err != nil {
        return 0, fmt.Errorf("error reading response body: %v", err)
    }
    
    var result map[string]interface{}
    if err := json.Unmarshal(bodyBytes, &result); err != nil {
        return 0, fmt.Errorf("error decoding response: %v", err)
    }

    hits, ok := result["hits"].([]interface{})
    if !ok {
        return 0, fmt.Errorf("unexpected response structure: hits not found or not an array")
    }

    count := int64(len(hits))  // นับจำนวน hits ที่ได้รับจริง

    for _, hitInterface := range hits {
        hit, ok := hitInterface.(map[string]interface{})
        if !ok {
            log.Printf("Skipping invalid hit structure")
            continue
        }

        entry := LogEntry{
            Username:        hit["username"].(string),
            ServiceProvider: hit["service_provider"].(string),
        }

        if timestampStr, ok := hit["timestamp"].(string); ok {
            if timestamp, err := time.Parse(time.RFC3339, timestampStr); err == nil {
                entry.Timestamp = timestamp
            } else {
                log.Printf("Error parsing timestamp: %v", err)
            }
        }

        resultChan <- entry
    }

    return count, nil  // คืนค่าจำนวน hits ที่นับได้จริง
}

// processResults processes the search results and updates the result struct
func processResults(resultChan <-chan LogEntry, result *Result, mu *sync.Mutex, startDate, endDate time.Time) {
    localUserDays := make(map[string]map[string]bool)
    localUsers := make(map[string]*UserStats)
    localProviders := make(map[string]*ProviderStats)

    for entry := range resultChan {
        if entry.Timestamp.Before(startDate) || entry.Timestamp.After(endDate) {
            continue // Skip entries outside the specified date range
        }

        // Process user stats
        if _, exists := localUsers[entry.Username]; !exists {
            localUsers[entry.Username] = &UserStats{
                DaysActive: 0,
                Providers:  make(map[string]bool),
            }
            localUserDays[entry.Username] = make(map[string]bool)
        }
        day := entry.Timestamp.Format("2006-01-02")
        if !localUserDays[entry.Username][day] {
            localUserDays[entry.Username][day] = true
            localUsers[entry.Username].DaysActive++
        }
        localUsers[entry.Username].Providers[entry.ServiceProvider] = true

        // Process provider stats
        if _, exists := localProviders[entry.ServiceProvider]; !exists {
            localProviders[entry.ServiceProvider] = &ProviderStats{
                Users: make(map[string]bool),
            }
        }
        localProviders[entry.ServiceProvider].Users[entry.Username] = true
    }

    // Merge local results into global result
    mu.Lock()
    defer mu.Unlock()
    for username, stats := range localUsers {
        if _, exists := result.Users[username]; !exists {
            result.Users[username] = stats
        } else {
            result.Users[username].DaysActive = stats.DaysActive // Use the correct count
            for provider := range stats.Providers {
                result.Users[username].Providers[provider] = true
            }
        }
    }
    for provider, stats := range localProviders {
        if _, exists := result.Providers[provider]; !exists {
            result.Providers[provider] = stats
        } else {
            for user := range stats.Users {
                result.Providers[provider].Users[user] = true
            }
        }
    }
}


// getDomain returns the full domain name based on the input
func getDomain(input string) string {
    if input == "etlr1" {
        return "etlr1.eduroam.org"
    }
    if input == "etlr2" {
        return "etlr2.eduroam.org"
    }
    return fmt.Sprintf("eduroam.%s", input)
}

// createSimplifiedOutputData creates a simplified output data structure
func createSimplifiedOutputData(result *Result, domain string, startDate, endDate time.Time, days int) SimplifiedOutputData {
    output := SimplifiedOutputData{}
    
    output.QueryInfo.Domain = domain
    output.QueryInfo.Days = days
    output.QueryInfo.StartDate = startDate.Format("2006-01-02 15:04:05")
    output.QueryInfo.EndDate = endDate.Format("2006-01-02 15:04:05")
    
    output.Description = "Aggregated Access-Accept events for the specified domain and time range."

    // Add summary
    output.Summary.TotalUsers = len(result.Users)
    output.Summary.TotalProviders = len(result.Providers)

    // Use a mutex to protect concurrent map access
    var mu sync.Mutex

    // Process provider stats
    output.ProviderStats = make([]struct {
        Provider  string   `json:"provider"`
        UserCount int      `json:"user_count"`
        Users     []string `json:"users"`
    }, 0, len(result.Providers))

    for provider, stats := range result.Providers {
        mu.Lock()
        users := make([]string, 0, len(stats.Users))
        for user := range stats.Users {
            users = append(users, user)
        }
        mu.Unlock()
        output.ProviderStats = append(output.ProviderStats, struct {
            Provider  string   `json:"provider"`
            UserCount int      `json:"user_count"`
            Users     []string `json:"users"`
        }{
            Provider:  provider,
            UserCount: len(users),
            Users:     users,
        })
    }

    // Sort provider stats by number of users
    sort.Slice(output.ProviderStats, func(i, j int) bool {
        return output.ProviderStats[i].UserCount > output.ProviderStats[j].UserCount
    })

    // Process user stats
    output.UserStats = make([]struct {
        Username   string   `json:"username"`
        DaysActive int      `json:"days_active"`
        Providers  []string `json:"providers"`
    }, 0, len(result.Users))

    for username, stats := range result.Users {
        mu.Lock()
        providers := make([]string, 0, len(stats.Providers))
        for provider := range stats.Providers {
            providers = append(providers, provider)
        }
        daysActive := stats.DaysActive
        mu.Unlock()
        output.UserStats = append(output.UserStats, struct {
            Username   string   `json:"username"`
            DaysActive int      `json:"days_active"`
            Providers  []string `json:"providers"`
        }{
            Username:   username,
            DaysActive: daysActive,
            Providers:  providers,
        })
    }

    // Sort user stats by days active
    sort.Slice(output.UserStats, func(i, j int) bool {
        return output.UserStats[i].DaysActive > output.UserStats[j].DaysActive
    })

    return output
}

// worker function to process jobs
func worker(job Job, resultChan chan<- LogEntry, query map[string]interface{}, props Properties) (int64, error) {
    currentQuery := make(map[string]interface{})
    for k, v := range query {
        currentQuery[k] = v
    }
    currentQuery["start_timestamp"] = job.StartTimestamp
    currentQuery["end_timestamp"] = job.EndTimestamp

    return getQuickwitResults(currentQuery, props, resultChan)
}

func main() {
    // Set logging flags
    log.SetFlags(log.LstdFlags | log.Lshortfile)
    
    // Record overall start time 
    overallStart := time.Now()

    if len(os.Args) < 2 || len(os.Args) > 3 {
        fmt.Println("Usage: ./eduroam-accept <domain> [days|DD-MM-YYYY]")
        os.Exit(1)
    }

    domain := os.Args[1]
    var startDate, endDate time.Time
    var days int
    var specificDate bool

    if len(os.Args) == 3 {
        if d, err := strconv.Atoi(os.Args[2]); err == nil && d <= 366 {
            // จำนวนวันถูกระบุ (ไม่เกิน 366 วัน)
            days = d
            endDate = time.Now()
            startDate = endDate.AddDate(0, 0, -days+1)
        } else {
            // วันที่เฉพาะถูกระบุในรูปแบบ DD-MM-YYYY
            specificDate = true
            var err error
            startDate, err = time.Parse("02-01-2006", os.Args[2])
            if err != nil {
                log.Fatalf("Invalid date format. Use DD-MM-YYYY: %v", err)
            }
            endDate = startDate.AddDate(0, 0, 1)
            days = 1
        }
    } else {
        // ค่าเริ่มต้น: 1 วัน
        days = 1
        endDate = time.Now()
        startDate = endDate.AddDate(0, 0, -1)
    }

    // ปรับเวลาให้ครอบคลุมทั้งวัน
    startDate = time.Date(startDate.Year(), startDate.Month(), startDate.Day(), 0, 0, 0, 0, startDate.Location())
    endDate = time.Date(endDate.Year(), endDate.Month(), endDate.Day(), 23, 59, 59, 999999999, endDate.Location())

    startTimestamp := startDate.Unix()
    endTimestamp := endDate.Unix()

    props, err := readProperties("qw-auth.properties")
    if err != nil {
        log.Fatalf("Error reading properties: %v", err)
    }

    if specificDate {
        log.Printf("Searching for date: %s", startDate.Format("2006-01-02"))
    } else {
        log.Printf("Searching from %s to %s", startDate.Format("2006-01-02"), endDate.Format("2006-01-02"))
    }

    query := map[string]interface{}{
        "query":           fmt.Sprintf(`message_type:"Access-Reject" AND realm:"%s" NOT service_provider:"client"`, getDomain(domain)),
        "start_timestamp": startTimestamp,
        "end_timestamp":   endTimestamp,
        "max_hits":        10000,
        "sort_by_field":   "_timestamp",
    }
    
    resultChan := make(chan LogEntry, 100)
    errChan := make(chan error, 1)

    result := &Result{
        Users:     make(map[string]*UserStats),
        Providers: make(map[string]*ProviderStats),
    }

    var totalHits atomic.Int64
    var mu sync.Mutex
    var wg sync.WaitGroup
    var processingWg sync.WaitGroup

    // Create job channel and worker pool
    jobs := make(chan Job, days)
    numWorkers := 5
    progressChan := make(chan int, days)
    var processedDays int32

    // Start worker pool
    for w := 1; w <= numWorkers; w++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for job := range jobs {
                hits, err := worker(job, resultChan, query, props)
                if err != nil {
                    errChan <- err
                    return
                }
                // ใช้ atomic.AddInt64 เพื่อบวกค่า hits เข้ากับ totalHits อย่างปลอดภัย
                totalHits.Add(hits)
                atomic.AddInt32(&processedDays, 1)
                fmt.Printf("\rProgress: %d/%d days processed, Total hits: %d", 
                    atomic.LoadInt32(&processedDays), 
                    days, 
                    totalHits.Load())
            }
        }()
    }

    // Start processing goroutine
    processingWg.Add(1)
    go func() {
        defer processingWg.Done()
        processResults(resultChan, result, &mu, startDate, endDate)
    }()

    // Start progress reporting goroutine
    go func() {
        for range progressChan {
            atomic.AddInt32(&processedDays, 1)
            fmt.Printf("\rProgress: %d/%d days processed", atomic.LoadInt32(&processedDays), days)
        }
    }()

    // Create and send jobs
    queryStart := time.Now()
    currentStartTimestamp := startTimestamp
    for currentStartTimestamp < endTimestamp {
        currentEndTimestamp := currentStartTimestamp + 24*60*60 // 1 day
        if currentEndTimestamp > endTimestamp {
            currentEndTimestamp = endTimestamp
        }
        jobs <- Job{StartTimestamp: currentStartTimestamp, EndTimestamp: currentEndTimestamp}
        currentStartTimestamp = currentEndTimestamp
    }
    close(jobs)

    // Wait for all workers to finish
    wg.Wait()
    close(resultChan)

    // Wait for processing goroutine to finish
    processingWg.Wait()

    // Check for errors
    select {
    case err := <-errChan:
        if err != nil {
            log.Printf("Error occurred: %v", err)
            return
        }
    default:
        // No error
    }

    queryDuration := time.Since(queryStart)

    fmt.Printf("\n") // New line after progress bar
    log.Printf("Total hits: %d", totalHits.Load())
    log.Printf("Number of users: %d", len(result.Users))
    log.Printf("Number of providers: %d", len(result.Providers))

    // Start measuring local processing time
    processStart := time.Now()

    // Create simplified output data
    outputData := createSimplifiedOutputData(result, domain, startDate, endDate, days)

    processDuration := time.Since(processStart)

    outputDir := fmt.Sprintf("output/%s", domain)
    if err := os.MkdirAll(outputDir, 0755); err != nil {
        log.Fatalf("Error creating output directory: %v", err)
    }

    // ปรับการตั้งชื่อไฟล์ output
    currentTime := time.Now().Format("20060102-150405")
    var filename string
    if specificDate {
        filename = fmt.Sprintf("%s/%s-%s.json", outputDir, currentTime, startDate.Format("20060102"))
    } else {
        filename = fmt.Sprintf("%s/%s-%dd.json", outputDir, currentTime, days)
    }

    jsonData, err := json.MarshalIndent(outputData, "", "  ")
    if err != nil {
        log.Fatalf("Error marshaling JSON: %v", err)
    }

    err = os.WriteFile(filename, jsonData, 0644)
    if err != nil {
        log.Fatalf("Error writing file: %v", err)
    }

    overallDuration := time.Since(overallStart)

    fmt.Printf("Results have been saved to %s\n", filename)
    fmt.Printf("Time taken:\n")
    fmt.Printf("  Quickwit query: %v\n", queryDuration)
    fmt.Printf("  Local processing: %v\n", processDuration)
    fmt.Printf("  Overall: %v\n", overallDuration)
}

