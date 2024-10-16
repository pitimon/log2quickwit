#!/bin/bash
set -e  # หยุดทันทีเมื่อเกิดข้อผิดพลาด

SOURCE_INDEX="nro-logs"
TARGET_INDEX="nro-logs-new"
BATCH_SIZE=10000  # ปรับเป็น 10,000 ตามข้อจำกัดของ Quickwit

# ฟังก์ชันสำหรับตรวจสอบว่า index มีอยู่จริงหรือไม่
check_index_exists() {
    if ! quickwit index list | grep -q "$1"; then
        echo "ไม่พบ index: $1"
        exit 1
    fi
}

# ตรวจสอบว่า index ต้นทางและปลายทางมีอยู่จริง
check_index_exists $SOURCE_INDEX
check_index_exists $TARGET_INDEX

# ดึงจำนวนเอกสารทั้งหมดโดยใช้ search API
TOTAL_DOCS=$(quickwit index search --index $SOURCE_INDEX --query "*" --max-hits 0 | grep -oP '"num_hits": \K\d+')

if [ -z "$TOTAL_DOCS" ] || [ "$TOTAL_DOCS" -eq 0 ]; then
    echo "ไม่สามารถดึงจำนวนเอกสารได้หรือ index ว่างเปล่า"
    exit 1
fi

echo "จำนวนเอกสารทั้งหมด: $TOTAL_DOCS"

PROCESSED=0

while [ $PROCESSED -lt $TOTAL_DOCS ]; do
    echo "กำลังประมวลผลเอกสาร $PROCESSED ถึง $((PROCESSED + BATCH_SIZE - 1))"

    # ดึงข้อมูลจาก index เก่า
    if ! quickwit index search --index $SOURCE_INDEX --query "*" --max-hits $BATCH_SIZE > temp_docs.json; then
        echo "เกิดข้อผิดพลาดในการค้นหาข้อมูล"
        exit 1
    fi

    # ตรวจสอบว่าได้ข้อมูลหรือไม่
    if [ ! -s temp_docs.json ]; then
        echo "ไม่พบข้อมูลในการค้นหา"
        break
    fi

    # แปลงข้อมูลให้อยู่ในรูปแบบที่เหมาะสมสำหรับการ ingest
    jq -c '.hits[]' temp_docs.json > temp_docs_for_ingest.json

    # ส่งข้อมูลไปยัง index ใหม่
    if ! quickwit index ingest --index $TARGET_INDEX --input-path temp_docs_for_ingest.json; then
        echo "เกิดข้อผิดพลาดในการนำเข้าข้อมูล"
        exit 1
    fi

    PROCESSED=$((PROCESSED + $(jq '.hits | length' temp_docs.json)))
    echo "ประมวลผลแล้ว $PROCESSED จาก $TOTAL_DOCS เอกสาร"

    # ลบไฟล์ชั่วคราว
    rm temp_docs.json temp_docs_for_ingest.json

    # หยุดพักระบบเล็กน้อยเพื่อลดภาระ
    sleep 1
done

echo "การ reindex เสร็จสมบูรณ์"