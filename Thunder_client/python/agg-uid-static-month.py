#!/usr/bin/env python3

import requests
from requests.auth import HTTPBasicAuth
from collections import defaultdict
import sys
import datetime
import re
import os
import json

def read_properties(file_path):
    properties = {}
    with open(file_path, 'r') as f:
        for line in f:
            line = line.strip()
            if line and not line.startswith('#'):
                key, value = line.split('=', 1)
                properties[key.strip()] = value.strip()
    return properties['QW_USER'], properties['QW_PASS']

def get_quickwit_results(query, auth):
    url = "https://quickwit.a.uni.net.th/api/v1/nro-logs/search"
    
    headers = {
        "Content-Type": "application/json",
        "Accept": "application/json"
    }
    
    response = requests.post(url, json=query, auth=auth, headers=headers)
    response.raise_for_status()
    return response.json()

def process_results(aggregations, domain):
    user_counts = defaultdict(int)
    pattern = re.compile(f"Access-Reject for user ([^@]+@{domain}\\.ac\\.th)")
    
    for bucket in aggregations['unique_users']['buckets']:
        match = pattern.search(bucket['key'])
        if match:
            user = match.group(1)
            user_counts[user] += bucket['doc_count']
    
    return dict(user_counts)

def main(domain):
    qw_user, qw_pass = read_properties('qw-auth.properties')
    auth = HTTPBasicAuth(qw_user, qw_pass)

    query = {
        "query": f"full_message:\"Access-Reject for user\" AND full_message:\"@{domain}\" AND full_message:\"from eduroam.{domain}\" AND timestamp:[2024-10-01T00:00:00Z TO 2024-10-31T23:59:59Z]",
        "max_hits": 0,
        "aggs": {
            "unique_users": {
                "terms": {
                    "field": "full_message",
                    "size": 65000
                }
            }
        }
    }

    try:
        quickwit_response = get_quickwit_results(query, auth)
        results = process_results(quickwit_response['aggregations'], domain)

        # สร้างไดเรกทอรีถ้ายังไม่มี
        if not os.path.exists(domain):
            os.makedirs(domain)

        current_time = datetime.datetime.now().strftime("%Y%m%d-%H%M%S")
        filename = f"{domain}/{current_time}.json"

        # เรียงลำดับผลลัพธ์และแปลงเป็น list of dictionaries
        sorted_results = [{"user": user, "count": count} for user, count in sorted(results.items(), key=lambda x: x[1], reverse=True)]

        # บันทึกผลลัพธ์เป็น JSON
        with open(filename, 'w') as f:
            json.dump(sorted_results, f, indent=2)
        
        print(f"Results have been saved to {filename}")

    except requests.exceptions.RequestException as e:
        print(f"An error occurred: {e}")

if __name__ == "__main__":
    if len(sys.argv) != 2:
        print("Usage: python agg-uid.py <domain>")
        sys.exit(1)
    
    domain = sys.argv[1]
    main(domain)