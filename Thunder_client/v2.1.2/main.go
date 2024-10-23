/*
Program: eduroam-accept (User Accept Roaming)
Version: 2.2.0
Description: This program aggregates Access-Accept events for users from a specified domain
             using the Quickwit search engine's aggregation capabilities. It collects data 
             over a specified time range, processes the results, and outputs the aggregated 
             data to a JSON file.

Usage: ./eduroam-accept <domain> [days|DD-MM-YYYY]
  <domain>: The domain to search for (e.g., 'example.ac.th' or 'etlr1' or 'etlr2')
  [days]: Optional. The number of days to look back from the current date. Default is 1. Max is 366.
  [DD-MM-YYYY]: Optional. A specific date to process data for.

Features:
- Efficient data aggregation using Quickwit's aggregation queries
- Optimized concurrent processing with worker pools
- Flexible time range specification: number of days or specific date
- Real-time progress reporting with accurate hit counts
- Streamlined output format focusing on essential information
- Enhanced performance through code optimization

Changes in version 2.2.0:
- Implemented Quickwit aggregation queries for improved data collection
- Enhanced performance with optimized worker pool management
- Streamlined code structure and reduced complexity
- Fixed date histogram aggregation using fixed intervals
- Improved progress reporting with accurate hit counting
- Reduced memory usage in data processing
- Simplified output format and console display

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
Date: [23 Oct 2024]
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
    Providers map[string]bool
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

// SimplifiedOutputData represents the output JSON structure
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
        Username  string   `json:"username"`
        Providers []string `json:"providers"`
    } `json:"user_stats"`
}

// Job represents a single day's query job
type Job struct {
    StartTimestamp int64
    EndTimestamp   int64
}

// sendQuickwitRequest handles HTTP communication with Quickwit
func sendQuickwitRequest(query map[string]interface{}, props Properties) (map[string]interface{}, error) {
    client := &http.Client{}
    jsonQuery, _ := json.Marshal(query)
    
    // Debug output if needed
    if os.Getenv("DEBUG") != "" {
        log.Printf("Query: %s", string(jsonQuery))
    }

    req, err := http.NewRequest("POST", props.QWURL+"/api/v1/nro-logs/search", strings.NewReader(string(jsonQuery)))
    if err != nil {
        return nil, fmt.Errorf("error creating request: %v", err)
    }

    req.SetBasicAuth(props.QWUser, props.QWPass)
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("Accept", "application/json")

    resp, err := client.Do(req)
    if err != nil {
        return nil, fmt.Errorf("error sending request: %v", err)
    }
    defer resp.Body.Close()

    bodyBytes, err := io.ReadAll(resp.Body)
    if err != nil {
        return nil, fmt.Errorf("error reading response: %v", err)
    }

    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("quickwit error (status %d): %s", resp.StatusCode, string(bodyBytes))
    }

    var result map[string]interface{}
    if err := json.Unmarshal(bodyBytes, &result); err != nil {
        return nil, fmt.Errorf("error decoding response: %v", err)
    }

    if errorMsg, hasError := result["error"].(string); hasError {
        return nil, fmt.Errorf("quickwit error: %s", errorMsg)
    }

    return result, nil
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

// worker processes a single job
func worker(job Job, resultChan chan<- LogEntry, query map[string]interface{}, props Properties) (int64, error) {
    currentQuery := map[string]interface{}{
        "query": query["query"],
        "start_timestamp": job.StartTimestamp,
        "end_timestamp": job.EndTimestamp,
        "max_hits": 0,
        "aggs": map[string]interface{}{
            "unique_users": map[string]interface{}{
                "terms": map[string]interface{}{
                    "field": "username",
                    "size": 10000,
                },
                "aggs": map[string]interface{}{
                    "providers": map[string]interface{}{
                        "terms": map[string]interface{}{
                            "field": "service_provider",
                            "size": 1000,
                        },
                    },
                    "daily": map[string]interface{}{
                        "date_histogram": map[string]interface{}{
                            "field": "timestamp",
                            "fixed_interval": "86400s",
                        },
                    },
                },
            },
        },
    }

    result, err := sendQuickwitRequest(currentQuery, props)
    if err != nil {
        return 0, err
    }

    return processAggregations(result, resultChan)
}

// processAggregations processes the aggregation results
func processAggregations(result map[string]interface{}, resultChan chan<- LogEntry) (int64, error) {
    aggs, ok := result["aggregations"].(map[string]interface{})
    if !ok {
        return 0, fmt.Errorf("no aggregations in response")
    }

    uniqueUsers, ok := aggs["unique_users"].(map[string]interface{})
    if !ok {
        return 0, fmt.Errorf("no unique_users aggregation")
    }

    buckets, ok := uniqueUsers["buckets"].([]interface{})
    if !ok {
        return 0, fmt.Errorf("no buckets in unique_users aggregation")
    }

    var totalHits int64
    for _, bucketInterface := range buckets {
        bucket, ok := bucketInterface.(map[string]interface{})
        if !ok {
            continue
        }

        username := bucket["key"].(string)
        docCount := int64(bucket["doc_count"].(float64))
        totalHits += docCount

        processUserBucket(bucket, username, resultChan)
    }

    return totalHits, nil
}

// processUserBucket processes a single user bucket from aggregations
func processUserBucket(bucket map[string]interface{}, username string, resultChan chan<- LogEntry) {
    if providersAgg, ok := bucket["providers"].(map[string]interface{}); ok {
        if providerBuckets, ok := providersAgg["buckets"].([]interface{}); ok {
            for _, providerBucketInterface := range providerBuckets {
                providerBucket, ok := providerBucketInterface.(map[string]interface{})
                if !ok {
                    continue
                }
                provider := providerBucket["key"].(string)
                processUserProviderDaily(bucket, username, provider, resultChan)
            }
        }
    }
}

// processUserProviderDaily processes daily activities for a user and provider
func processUserProviderDaily(bucket map[string]interface{}, username, provider string, resultChan chan<- LogEntry) {
    if dailyAgg, ok := bucket["daily"].(map[string]interface{}); ok {
        if dailyBuckets, ok := dailyAgg["buckets"].([]interface{}); ok {
            for _, dailyBucketInterface := range dailyBuckets {
                dailyBucket, ok := dailyBucketInterface.(map[string]interface{})
                if !ok || dailyBucket["doc_count"].(float64) == 0 {
                    continue
                }

                timestamp := time.Unix(int64(dailyBucket["key"].(float64)/1000), 0)
                resultChan <- LogEntry{
                    Username:        username,
                    ServiceProvider: provider,
                    Timestamp:      timestamp,
                }
            }
        }
    }
}

// processResults processes the search results and updates the result struct
func processResults(resultChan <-chan LogEntry, result *Result, mu *sync.Mutex) {
    userMap := make(map[string]map[string]bool)
    for entry := range resultChan {
        if _, exists := userMap[entry.Username]; !exists {
            userMap[entry.Username] = make(map[string]bool)
        }
        userMap[entry.Username][entry.ServiceProvider] = true
    }

    mu.Lock()
    defer mu.Unlock()

    for username, providers := range userMap {
        if _, exists := result.Users[username]; !exists {
            result.Users[username] = &UserStats{
                Providers: make(map[string]bool),
            }
        }

        for provider := range providers {
            result.Users[username].Providers[provider] = true
            
            if _, exists := result.Providers[provider]; !exists {
                result.Providers[provider] = &ProviderStats{
                    Users: make(map[string]bool),
                }
            }
            result.Providers[provider].Users[username] = true
        }
    }
}

// createOutputData creates the output JSON structure
func createOutputData(result *Result, domain string, startDate, endDate time.Time, days int) SimplifiedOutputData {
    output := SimplifiedOutputData{}
    output.QueryInfo.Domain = domain
    output.QueryInfo.Days = days
    output.QueryInfo.StartDate = startDate.Format("2006-01-02 15:04:05")
    output.QueryInfo.EndDate = endDate.Format("2006-01-02 15:04:05")
    output.Description = "Aggregated Access-Accept events for the specified domain and time range."

    output.Summary.TotalUsers = len(result.Users)
    output.Summary.TotalProviders = len(result.Providers)

    // Process provider stats
    output.ProviderStats = make([]struct {
        Provider  string   `json:"provider"`
        UserCount int      `json:"user_count"`
        Users     []string `json:"users"`
    }, 0, len(result.Providers))

    for provider, stats := range result.Providers {
        users := make([]string, 0, len(stats.Users))
        for user := range stats.Users {
            users = append(users, user)
        }
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
        Username  string   `json:"username"`
        Providers []string `json:"providers"`
    }, 0, len(result.Users))

    for username, stats := range result.Users {
        providers := make([]string, 0, len(stats.Providers))
        for provider := range stats.Providers {
            providers = append(providers, provider)
        }
        output.UserStats = append(output.UserStats, struct {
            Username  string   `json:"username"`
            Providers []string `json:"providers"`
        }{
            Username:  username,
            Providers: providers,
        })
    }

    // Sort user stats by username
    sort.Slice(output.UserStats, func(i, j int) bool {
        return output.UserStats[i].Username < output.UserStats[j].Username
    })

    return output
}

func main() {
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
            days = d
            endDate = time.Now()
            startDate = endDate.AddDate(0, 0, -days+1)
        } else {
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
        days = 1
        endDate = time.Now()
        startDate = endDate.AddDate(0, 0, -1)
    }

    startDate = time.Date(startDate.Year(), startDate.Month(), startDate.Day(), 0, 0, 0, 0, startDate.Location())
    endDate = time.Date(endDate.Year(), endDate.Month(), endDate.Day(), 23, 59, 59, 999999999, endDate.Location())

    props, err := readProperties("qw-auth.properties")
    if err != nil {
        log.Fatalf("Error reading properties: %v", err)
    }

    if specificDate {
        fmt.Printf("Searching for date: %s\n", startDate.Format("2006-01-02"))
    } else {
        fmt.Printf("Searching from %s to %s\n", startDate.Format("2006-01-02"), endDate.Format("2006-01-02"))
    }

    query := map[string]interface{}{
        "query":           fmt.Sprintf(`message_type:"Access-Accept" AND realm:"%s" NOT service_provider:"client"`, getDomain(domain)),
        "start_timestamp": startDate.Unix(),
        "end_timestamp":   endDate.Unix(),
        "max_hits":        10000,
    }

    resultChan := make(chan LogEntry, 10000)
    errChan := make(chan error, 1)
    var totalHits atomic.Int64
    var mu sync.Mutex
    var wg sync.WaitGroup

    jobs := make(chan Job, days)
    numWorkers := 10

    var processedDays int32
    queryStart := time.Now()

    // Start worker pool
    for w := 1; w <= numWorkers; w++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for job := range jobs {
                hits, err := worker(job, resultChan, query, props)
                if err != nil {
                    select {
                    case errChan <- err:
                    default:
                    }
                    return
                }
                totalHits.Add(hits)
                current := atomic.AddInt32(&processedDays, 1)
                fmt.Printf("\rProgress: %d/%d days processed, Progress hits: %d", 
                    current, days, totalHits.Load())
            }
        }()
    }

    result := &Result{
        Users:     make(map[string]*UserStats),
        Providers: make(map[string]*ProviderStats),
    }

    processDone := make(chan struct{})
    go func() {
        processResults(resultChan, result, &mu)
        close(processDone)
    }()

    currentDate := startDate
    for currentDate.Before(endDate) {
        nextDate := currentDate.Add(24 * time.Hour)
        if nextDate.After(endDate) {
            nextDate = endDate
        }
        jobs <- Job{
            StartTimestamp: currentDate.Unix(),
            EndTimestamp:   nextDate.Unix(),
        }
        currentDate = nextDate
    }
    close(jobs)

    wg.Wait()
    close(resultChan)

    <-processDone

    select {
    case err := <-errChan:
        if err != nil {
            log.Fatalf("Error occurred: %v", err)
        }
    default:
    }

    queryDuration := time.Since(queryStart)

    fmt.Printf("\n")
    fmt.Printf("Number of users: %d\n", len(result.Users))
    fmt.Printf("Number of providers: %d\n", len(result.Providers))

    processStart := time.Now()
    outputData := createOutputData(result, domain, startDate, endDate, days)
    processDuration := time.Since(processStart)

    outputDir := fmt.Sprintf("output/%s", domain)
    if err := os.MkdirAll(outputDir, 0755); err != nil {
        log.Fatalf("Error creating output directory: %v", err)
    }

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

    if err := os.WriteFile(filename, jsonData, 0644); err != nil {
        log.Fatalf("Error writing file: %v", err)
    }

    fmt.Printf("Results have been saved to %s\n", filename)
    fmt.Printf("Time taken:\n")
    fmt.Printf("  Quickwit query: %v\n", queryDuration)
    fmt.Printf("  Local processing: %v\n", processDuration)
    fmt.Printf("  Overall: %v\n", time.Since(queryStart))
}
