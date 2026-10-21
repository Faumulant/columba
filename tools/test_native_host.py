"""按浏览器真实方式测试 Native Messaging 宿主：带 origin 参数启动。

用法: python tools/test_native_host.py [exe] [origin]

为什么必须带 origin：Chrome/Edge/Firefox 拉起宿主时第一个参数是调用方 origin
（chrome-extension://<id>/）。早先的测试脚本不带参数启动，恰好走了另一条分支，
导致「真实调用挂掉、自测通过」——这个坑已经踩过一次，测试必须模拟真实调用。
"""
import json, struct, subprocess, os, sys

DEFAULT_EXE = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "dropw.exe")
exe = sys.argv[1] if len(sys.argv) > 1 else DEFAULT_EXE
origin = sys.argv[2] if len(sys.argv) > 2 else "chrome-extension://pgihdeeaofkecmpdldpagfhnehmibgkf/"
if not os.path.exists(exe):
    sys.exit("找不到 %s：先构建，或把 dropw.exe 路径作为参数传入" % exe)

# 关键：带 origin 参数，模拟浏览器真实调用
proc = subprocess.Popen([exe, origin],
                        stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)

def send(req):
    raw = json.dumps(req).encode('utf-8')
    proc.stdin.write(struct.pack('<I', len(raw)) + raw)
    proc.stdin.flush()
    hdr = proc.stdout.read(4)
    if len(hdr) < 4:
        return None
    n = struct.unpack('<I', hdr)[0]
    return json.loads(proc.stdout.read(n))

r1 = send({'path': '/api/state', 'method': 'GET'})
if r1 is None:
    print("FAIL: 宿主没有响应（启动后立即退出）")
    err = proc.stderr.read(800).decode('utf-8', errors='replace')
    print("stderr:", err.strip() or "(空)")
    proc.kill()
    sys.exit(1)

print("state ok:", r1.get('ok'))
devs = (r1.get('data') or {}).get('devices') or []
print("devices:", [(d['name'], d['online']) for d in devs])

r2 = send({'path': '/api/send-text', 'method': 'POST', 'body': {'text': '来自扩展的测试消息'}})
print("send-text ok:", r2.get('ok') if r2 else None)

if devs:
    r3 = send({'path': '/api/select', 'method': 'POST', 'body': {'id': devs[0]['id']}})
    print("select ok:", r3.get('ok') if r3 else None)

proc.stdin.close()
try:
    proc.wait(timeout=5)
except Exception:
    proc.kill()
print("host 退出码:", proc.returncode)
