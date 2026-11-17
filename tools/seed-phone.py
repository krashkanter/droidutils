import json,sys,urllib.request
root='/data/local/tmp/sysutils-presets/'
token=sys.stdin.read().strip()
def api(method,path,body=None):
    req=urllib.request.Request('http://127.0.0.1:9474/v1/'+path,data=json.dumps(body).encode() if body is not None else None,method=method,headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'})
    with urllib.request.urlopen(req,timeout=20) as r:return json.load(r)
# Files are provisioned by rooted ADB into the chroot's /opt/sysutils-presets.
for filename,name,order,timeout in [('quiet-hardware.sh','Quiet hardware',10,100),('server-stack.sh','Server stack',20,300)]:
    body=open('/opt/sysutils-presets/'+filename).read()
    rows=api('GET','scripts')
    old=next((v for v in rows if v['name']==name),None)
    payload={'name':name,'body':body,'order':order,'timeout':timeout,'enabled':True}
    api('PUT' if old else 'POST','scripts/'+old['id'] if old else 'scripts',payload)
# A harmless witness confirms execution came from the new boot without ADB startup.
body='#!/system/bin/sh\necho "$SYSUTILS_BOOT_ID" > /data/local/sysutils/boot-witness\nid\n'
api('POST','scripts',{'name':'Boot verification','body':body,'order':9999,'timeout':10,'enabled':True})
print('Provisioned startup scripts')