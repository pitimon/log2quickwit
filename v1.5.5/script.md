```
export QW_USER=
export QW_PASS=
curl -u $QW_USER:$QW_PASS 'https://quickwit.a.uni.net.th/api/v1/indexes'


# สร้าง
export QW_USER=
export QW_PASS=
curl -X POST -u $QW_USER:$QW_PASS -H "Content-Type: application/json" -d @nro-logs-config.json 'https://quickwit.a.uni.net.th/api/v1/indexes'



#ลบ
curl -u $QW_USER:$QW_PASS -XDELETE https://quickwit.a.uni.net.th/api/v1/indexes/nro-logs-temp

# ตรวจสอบผลลัพธ์
curl -u $QW_USER:$QW_PASS 'https://quickwit.a.uni.net.th/api/v1/nro-logs/search?query=*&max_hits=5'
```