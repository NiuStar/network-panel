docker build -t network-panel:latest .
#docker pull registry.cn-hangzhou.aliyuncs.com/nqc/arkoselabs_token_api.v2:latest
docker tag network-panel:latest 24802117/network-panel:latest
docker push 24802117/network-panel:latest

