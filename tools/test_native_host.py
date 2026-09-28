import json, struct, subprocess, os, sys

# 模拟浏览器：以 Native Messaging 协议与 dropw.exe 通信
proc = subprocess.Popen([r'<仓库>\dropw.exe'],
                        stdin=subprocess.PIPE, stdout=subprocess.PIPE)

def send(req):
    raw = json.dumps(req).encode('utf-8')
    proc.stdin.write(struct.pack('<I', len(raw)) + raw)
    proc.stdin.flush()
    n = struct.unpack('<I', proc.stdout.read(4))[0]
    return json.loads(proc.stdout.read(n))

# 1) 状态查询
r1 = send({'path': '/api/state', 'method': 'GET'})
print('state ok:', r1['ok'], '| 设备:', [(d['name'], d['online']) for d in r1['data']['devices']])

# 2) 发文本（目标未在线 → 应返回结构化错误）
r2 = send({'path': '/api/send-text', 'method': 'POST',
           'body': {'text': '来自浏览器扩展的测试消息'}})
print('send-text ok:', r2['ok'], '| message:', r2.get('data', {}).get('message', r2.get('message', ''))[:50])

# 3) 选择目标为手机后再发（手机不在线 → 结构化错误，链路已验证）
r3 = send({'path': '/api/select', 'method': 'POST',
           'body': {'id': '<设备指纹前缀>B584EE8664EE2CE836AED64688B6ED365DE9B87E1924959093262CA1'}})
print('select ok:', r3['ok'])

proc.stdin.close()
proc.wait(timeout=5)
print('host 正常退出, exit =', proc.returncode)
