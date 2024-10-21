#!/usr/bin/env python3

import requests
from requests.auth import HTTPBasicAuth
from collections import defaultdict
import sys

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

def process_results(aggregations):
    user_counts = defaultdict(int)
    for bucket in aggregations['unique_users']['buckets']:
        user = bucket['key']
        user_counts[user] += bucket['doc_count']
    return dict(user_counts)

def main(domain):
    # อ่านค่า user และ password จาก properties file
    qw_user, qw_pass = read_properties('qw-auth.properties')
    auth = HTTPBasicAuth(qw_user, qw_pass)

    # สร้าง query โดยใช้ domain ที่รับมาเป็น parameter
    query = {
        "query": f"full_message:\"Access-Reject for user\" AND full_message:\"@{domain}\" AND full_message:\"from eduroam.{domain}\" AND timestamp:[2024-10-01T00:00:00Z TO 2024-10-31T23:59:59Z]",
        "max_hits": 0,
        "aggs": {
            "unique_users": {
                "terms": {
                    "field": "full_message",
                    "regex": f"Access-Reject for user ([^@]+@{domain}\\.ac\\.th)",
                    "size": 65000
                }
            }
        }
    }

    try:
        quickwit_response = get_quickwit_results(query, auth)
        results = process_results(quickwit_response['aggregations'])

        for user, count in results.items():
            print(f"{user}: {count}")
    except requests.exceptions.RequestException as e:
        print(f"An error occurred: {e}")

if __name__ == "__main__":
    if len(sys.argv) != 2:
        print("Usage: python agg-uid.py <domain>")
        sys.exit(1)
    
    domain = sys.argv[1]
    main(domain)