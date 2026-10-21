import json, struct, subprocess, os, sys

# 模拟浏览器：以 Native Messaging 协议与 dropw.exe 通信。
# 用法：python tools/test_native_host.py [dropw.exe 路径]
# 不给路径时按「与本脚本同级的仓库根目录」推断，避免把本机绝对路径写进仓库。
DEFAULT_EXE = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "dropw.exe")
exe = sys.argv[1] if len(sys.argv) > 1 else DEFAULT_EXE
if not os.path.exists(exe):
    sys.exit(f"找不到 {exe}：先构建，或把 dropw.exe 路径作为参数传入")

proc = subprocess.Popen([exe],
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

# 3) 选一个已配对设备作目标（取设备列表第一个，不写死任何真实指纹）
devices = r1['data']['devices']
if devices:
    r3 = send({'path': '/api/select', 'method': 'POST',
               'body': {'id': devices[0]['id']}})
    print('select ok:', r3['ok'])
else:
    print('select skipped: 没有已配对设备')

proc.stdin.close()
proc.wait(timeout=5)
print('host 正常退出, exit =', proc.returncode)
