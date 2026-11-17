import json, sys, time, urllib.request, urllib.error
TOKEN=sys.stdin.read().strip()
BASE='http://127.0.0.1:9474/v1/'
def api(method,path,body=None,auth=True):
    req=urllib.request.Request(BASE+path,data=json.dumps(body).encode() if body is not None else None,method=method)
    if auth:req.add_header('Authorization','Bearer '+TOKEN)
    req.add_header('Content-Type','application/json')
    with urllib.request.urlopen(req,timeout=20) as response:return json.load(response)
try:
    api('GET','status',auth=False)
    raise AssertionError('Unauthenticated API accepted')
except urllib.error.HTTPError as e:assert e.code==401
status=api('GET','status');assert status['uid']==0
assert len(api('GET','services'))>10
assert len(api('GET','settings?namespace=global'))>10
script=None
try:
    script=api('POST','scripts',{'name':'SysUtils smoke','body':'#!/system/bin/sh\nid\necho sysutils-ok\n','timeout':5,'order':9999,'enabled':False})
    id=script['id']
    api('POST',f'scripts/{id}/run',{})
    for _ in range(40):
        result=api('GET',f'scripts/{id}/logs')
        if not result['result']['running']:break
        time.sleep(.25)
    assert result['result']['exit']==0,result
    assert 'uid=0' in result['text'] and 'sysutils-ok' in result['text'],result
    script['name']='SysUtils edited';api('PUT',f'scripts/{id}',script)
    assert api('GET',f'scripts/{id}')['name']=='SysUtils edited'
finally:
    if script:api('DELETE','scripts/'+script['id'])
key='sysutils_install_smoke'
existing=next((r for r in api('GET','settings?namespace=global') if r['key']==key),None)
assert existing is None,'Test key unexpectedly exists'
try:
    value='literal; no shell expansion'
    api('PUT','settings',{'namespace':'global','key':key,'value':value})
    assert next(r for r in api('GET','settings?namespace=global') if r['key']==key)['value']==value
finally:api('PUT','settings',{'namespace':'global','key':key,'delete':True})
print('PASS: authentication, root execution, script CRUD, logs, settings roundtrip, service inventory')