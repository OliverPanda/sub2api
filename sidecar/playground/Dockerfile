FROM node:20-alpine

WORKDIR /app
COPY package.json server.mjs index.html ./

ENV PORT=8081
ENV SUB2API_UPSTREAM=http://sub2api:8080
ENV PLAYGROUND_API_BASE_URL=https://api.xddapi.org.uk/v1

EXPOSE 8081
HEALTHCHECK --interval=30s --timeout=5s --retries=3 CMD wget -q -T 5 -O /dev/null http://127.0.0.1:8081/health || exit 1
CMD ["node", "server.mjs"]
