/*
Program: eduroam-sp (Service Provider Accept Analysis)
Version: 2.2.1
Description: This program analyzes Access-Accept events for a specified service provider
             using the Quickwit search engine's aggregation capabilities. It collects data 
             over a specified time range, processes the results by realms and users, and
             outputs the aggregated data to a JSON file.

Major changes in v2.2.1:
1. Changed query focus from realm to service_provider
2. Restructured aggregation to group users by realm
3. Added active_days count for each user
4. Modified output format to show realm groupings and user activity
5. Improved timestamp handling for daily activity counting
6. Enhanced performance with optimized aggregation queries
7. Updated progress reporting for service provider context

Usage: ./eduroam-sp <service_provider> [days|Ny|DD-MM-YYYY]
      <service_provider>: The service provider to search for (e.g., 'eduroam.ku.ac.th')
      [days]: Optional. The number of days (1-3650) to look back from the current date.
      [Ny]: Optional. The number of years (1y-10y) to look back from the current date.
      [DD-MM-YYYY]: Optional. A specific date to process data for.

Author: [P.Itarun]
Date: October 23, 2024
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
    Realm          string    `json:"realm"`
    ServiceProvider string    `json:"service_provider"`
    Timestamp      time.Time `json:"timestamp"`
}

// UserStats contains statistics for a user
type UserStats struct {
    Username    string
    ActiveDays  int
}

// RealmStats contains statistics for a realm
type RealmStats struct {
    Realm     string
    Users     map[string]bool
}

// Result holds the aggregated results
type Result struct {
    Users     map[string]*UserStats
    Realms    map[string]*RealmStats
}

// SimplifiedOutputData represents the output JSON structure
type SimplifiedOutputData struct {
    QueryInfo struct {
        ServiceProvider string `json:"service_provider"`
        Days           int    `json:"days"`
        StartDate      string `json:"start_date"`
        EndDate        string `json:"end_date"`
    } `json:"query_info"`
    Description   string `json:"description"`
    Summary       struct {
        TotalUsers  int `json:"total_users"`
        TotalRealms int `json:"total_realms"`
    } `json:"summary"`
    RealmStats []struct {
        Realm     string   `json:"realm"`
        UserCount int      `json:"user_count"`
        Users     []string `json:"users"`
    } `json:"realm_stats"`
    UserStats []struct {
        Username   string `json:"username"`
        ActiveDays int    `json:"active_days"`
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
                    "realms": map[string]interface{}{
                        "terms": map[string]interface{}{
                            "field": "realm",
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
    if realmsAgg, ok := bucket["realms"].(map[string]interface{}); ok {
        if realmBuckets, ok := realmsAgg["buckets"].([]interface{}); ok {
            for _, realmBucketInterface := range realmBuckets {
                realmBucket, ok := realmBucketInterface.(map[string]interface{})
                if !ok {
                    continue
                }
                realm := realmBucket["key"].(string)
                processUserRealmDaily(bucket, username, realm, resultChan)
            }
        }
    }
}

// processUserRealmDaily processes daily activities for a user and realm
func processUserRealmDaily(bucket map[string]interface{}, username string, realm string, resultChan chan<- LogEntry) {
    if dailyAgg, ok := bucket["daily"].(map[string]interface{}); ok {
        if dailyBuckets, ok := dailyAgg["buckets"].([]interface{}); ok {
            for _, dailyBucketInterface := range dailyBuckets {
                dailyBucket, ok := dailyBucketInterface.(map[string]interface{})
                if !ok || dailyBucket["doc_count"].(float64) == 0 {
                    continue
                }

                timestamp := time.Unix(int64(dailyBucket["key"].(float64)/1000), 0)
                resultChan <- LogEntry{
                    Username: username,
                    Realm:    realm,
                    Timestamp: timestamp,
                }
            }
        }
    }
}

// processResults processes the search results and updates the result struct
func processResults(resultChan <-chan LogEntry, result *Result, mu *sync.Mutex) {
    activeDays := make(map[string]map[string]bool) // username -> date -> bool
    
    for entry := range resultChan {
        dateStr := entry.Timestamp.Format("2006-01-02")
        
        // Initialize maps if they don't exist
        if _, exists := activeDays[entry.Username]; !exists {
            activeDays[entry.Username] = make(map[string]bool)
        }
        activeDays[entry.Username][dateStr] = true

        mu.Lock()
        // Update realm stats
        if _, exists := result.Realms[entry.Realm]; !exists {
            result.Realms[entry.Realm] = &RealmStats{
                Realm: entry.Realm,
                Users: make(map[string]bool),
            }
        }
        result.Realms[entry.Realm].Users[entry.Username] = true
        mu.Unlock()
    }

    mu.Lock()
    defer mu.Unlock()

    // Process active days for each user
    for username, dates := range activeDays {
        result.Users[username] = &UserStats{
            Username:   username,
            ActiveDays: len(dates),
        }
    }
}

// createOutputData creates the output JSON structure
func createOutputData(result *Result, serviceProvider string, startDate, endDate time.Time, days int) SimplifiedOutputData {
    output := SimplifiedOutputData{}
    
    // Set query info
    output.QueryInfo.ServiceProvider = serviceProvider
    output.QueryInfo.Days = days
    output.QueryInfo.StartDate = startDate.Format("2006-01-02 15:04:05")
    output.QueryInfo.EndDate = endDate.Format("2006-01-02 15:04:05")
    output.Description = "Aggregated Access-Accept events for the specified service provider and time range."

    // Set summary
    output.Summary.TotalUsers = len(result.Users)
    output.Summary.TotalRealms = len(result.Realms)

    // Process realm stats
    output.RealmStats = make([]struct {
        Realm     string   `json:"realm"`
        UserCount int      `json:"user_count"`
        Users     []string `json:"users"`
    }, 0, len(result.Realms))

    for realm, stats := range result.Realms {
        users := make([]string, 0, len(stats.Users))
        for user := range stats.Users {
            users = append(users, user)
        }
        sort.Strings(users)
        
        output.RealmStats = append(output.RealmStats, struct {
            Realm     string   `json:"realm"`
            UserCount int      `json:"user_count"`
            Users     []string `json:"users"`
        }{
            Realm:     realm,
            UserCount: len(users),
            Users:     users,
        })
    }

    // Sort realm stats by user count (descending)
    sort.Slice(output.RealmStats, func(i, j int) bool {
        return output.RealmStats[i].UserCount > output.RealmStats[j].UserCount
    })

    // Process user stats
    output.UserStats = make([]struct {
        Username   string `json:"username"`
        ActiveDays int    `json:"active_days"`
    }, 0, len(result.Users))

    for _, stats := range result.Users {
        output.UserStats = append(output.UserStats, struct {
            Username   string `json:"username"`
            ActiveDays int    `json:"active_days"`
        }{
            Username:   stats.Username,
            ActiveDays: stats.ActiveDays,
        })
    }

    // Sort user stats by active days (descending) and then by username
    sort.Slice(output.UserStats, func(i, j int) bool {
        if output.UserStats[i].ActiveDays != output.UserStats[j].ActiveDays {
            return output.UserStats[i].ActiveDays > output.UserStats[j].ActiveDays
        }
        return output.UserStats[i].Username < output.UserStats[j].Username
    })

    return output
}

// getDomain returns the full domain name for service provider
func getDomain(input string) string {
    // Special cases
    switch input {
    case "etlr1":
        return "etlr1.eduroam.org"
    case "etlr2":
        return "etlr2.eduroam.org"
    }
    
    // If input already contains "eduroam.", return as is
    if strings.HasPrefix(input, "eduroam.") {
        return input
    }
    
    // For all other cases, add "eduroam." prefix
    return fmt.Sprintf("eduroam.%s", input)
}

func main() {
	if len(os.Args) < 2 || len(os.Args) > 3 {
		fmt.Println("Usage: ./eduroam-sp <service_provider> [days|Ny|yxxxx|DD-MM-YYYY]")
		fmt.Println("  service_provider: domain name (e.g., 'ku.ac.th', 'etlr1')")
		fmt.Println("  days: number of days (1-3650)")
		fmt.Println("  Ny: number of years (1y-10y)")
		fmt.Println("  yxxxx: specific year (e.g., y2024)")
		fmt.Println("  DD-MM-YYYY: specific date")
		os.Exit(1)
	}
 
	// ประกาศตัวแปร
	var serviceProvider string
	var startDate, endDate time.Time
	var days int
	var specificDate bool
 
	// กำหนดค่า serviceProvider
	serviceProvider = getDomain(os.Args[1])
 
	if len(os.Args) == 3 {
		param := os.Args[2]
		
		// เพิ่มการตรวจสอบรูปแบบ yxxxx สำหรับปี
		if strings.HasPrefix(param, "y") && len(param) == 5 {
			yearStr := param[1:]  // ตัด y ออกเพื่อเอาเฉพาะตัวเลขปี
			if year, err := strconv.Atoi(yearStr); err == nil {
				// ตรวจสอบว่าเป็นปีที่มีเหตุผล (เช่น 2000-2100)
				if year >= 2000 && year <= 2100 {
					// กำหนดช่วงวันที่สำหรับปีที่ระบุ
					startDate = time.Date(year, 1, 1, 0, 0, 0, 0, time.Local)
					endDate = time.Date(year, 12, 31, 23, 59, 59, 999999999, time.Local)
					days = 365
					if isLeapYear(year) {
						days = 366
					}
				} else {
					log.Fatalf("Invalid year range. Must be between 2000 and 2100")
				}
			} else {
				log.Fatalf("Invalid year format. Use y followed by 4 digits (e.g., y2024)")
			}
		} else if strings.HasSuffix(param, "y") {
			yearStr := strings.TrimSuffix(param, "y")
			if years, err := strconv.Atoi(yearStr); err == nil {
				if years >= 1 && years <= 10 {
					days = years * 365
					endDate = time.Now()
					startDate = endDate.AddDate(0, 0, -days+1)
				} else {
					log.Fatalf("Invalid year range. Must be between 1y and 10y")
				}
			} else {
				log.Fatalf("Invalid year format. Use 1y-10y")
			}
		} else if d, err := strconv.Atoi(param); err == nil {
			if d >= 1 && d <= 3650 {
				days = d
				endDate = time.Now()
				startDate = endDate.AddDate(0, 0, -days+1)
			} else {
				log.Fatalf("Invalid number of days. Must be between 1 and 3650")
			}
		} else {
			specificDate = true
			var err error
			startDate, err = time.Parse("02-01-2006", param)
			if err != nil {
				log.Fatalf("Invalid date format. Use DD-MM-YYYY: %v", err)
			}
			endDate = startDate.AddDate(0, 0, 1)
			days = 1
		}
	} else {
		// Default: 1 day
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
		"query":           fmt.Sprintf(`message_type:"Access-Accept" AND service_provider:"%s"`, serviceProvider),
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
 
	result := &Result{
		Users:  make(map[string]*UserStats),
		Realms: make(map[string]*RealmStats),
	}
 
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
	fmt.Printf("Number of realms: %d\n", len(result.Realms))
 
	processStart := time.Now()
	outputData := createOutputData(result, serviceProvider, startDate, endDate, days)
	processDuration := time.Since(processStart)
 
	outputDir := fmt.Sprintf("output/%s", strings.Replace(serviceProvider, ".", "-", -1))
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		log.Fatalf("Error creating output directory: %v", err)
	}
 
	currentTime := time.Now().Format("20060102-150405")
    var filename string
    if specificDate {
        filename = fmt.Sprintf("%s/%s-%s.json", outputDir, currentTime, startDate.Format("20060102"))
    } else if len(os.Args) > 2 && strings.HasPrefix(os.Args[2], "y") && len(os.Args[2]) == 5 {
        // กรณี yxxxx
        year := os.Args[2][1:] // ตัด y ออกเหลือแค่ปี
        filename = fmt.Sprintf("%s/%s-%s.json", outputDir, currentTime, year)
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

// เพิ่มฟังก์ชันสำหรับตรวจสอบปีอธิกสุรทิน
func isLeapYear(year int) bool {
    return year%4 == 0 && (year%100 != 0 || year%400 == 0)
}