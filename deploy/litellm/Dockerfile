# Build once, run the contract checks, then deploy the resulting immutable image.
FROM python:3.13-slim@sha256:70729b46c69b4f1e97c4822c1af3df53a1476cf5ddc6c087c0c10bc3a5678c2f
ARG LITELLM_VERSION=1.110.0
RUN pip install --no-cache-dir "litellm[proxy]==${LITELLM_VERSION}"
ENV LITELLM_LOCAL_MODEL_COST_MAP=True
RUN python -c "from litellm.llms.minimax.videos.transformation import MinimaxVideoConfig; from litellm.llms.volcengine.videos.transformation import VolcEngineVideoConfig"
COPY verify_contract.py /opt/media/verify_contract.py
EXPOSE 4000
ENTRYPOINT ["litellm"]
