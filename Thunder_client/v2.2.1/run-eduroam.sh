#!/bin/bash

# ตรวจสอบว่ามีการระบุ service_provider หรือไม่
if [ $# -ne 1 ]; then
    echo "Usage: $0 <service_provider>"
    echo "Example: $0 swu.ac.th"
    exit 1
fi

SERVICE_PROVIDER=$1

# กำหนด array ของช่วงเวลาที่ต้องการ
PERIODS=("" "7" "14" "30" "90" "180" "1y" "10y")

# วนลูปรันคำสั่งสำหรับแต่ละช่วงเวลา
for period in "${PERIODS[@]}"; do
    if [ -z "$period" ]; then
        echo "Running: ./eduroam-sp $SERVICE_PROVIDER"
        ./eduroam-sp "$SERVICE_PROVIDER"
    else
        echo "Running: ./eduroam-sp $SERVICE_PROVIDER $period"
        ./eduroam-sp "$SERVICE_PROVIDER" "$period"
    fi
    
    # รอ 2 วินาทีระหว่างคำสั่ง
    sleep 2
done
