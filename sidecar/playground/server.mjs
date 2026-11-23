import { createServer } from 'node:http'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import path from 'node:path'

const root = path.dirname(fileURLToPath(import.meta.url))
const port = Number.parseInt(process.env.PORT || '8081', 10)
const upstream = (process.env.SUB2API_UPSTREAM || 'http://sub2api:8080').replace(/\/$/, '')
const apiBaseUrl = (process.env.PLAYGROUND_API_BASE_URL || 'https://api.xddapi.org.uk/v1').replace(/\/$/, '')
const maxBodyBytes = 16 * 1024 * 1024

const allowedPaths = [
  /^\/images\/(generations|edits)$/,
  /^\/videos(?:\/generations)?$/,
  /^\/videos\/(?:[A-Za-z0-9:_-]|%2F|%2f)+(?:\/content)?$/
]

function json(res, status, data) {
  const body = JSON.stringify(data)
  res.writeHead(status, {
    'Cache-Control': 'no-store',
    'Content-Type': 'application/json; charset=utf-8',
    'Content-Length': Buffer.byteLength(body)
  })
  res.end(body)
}

function readBody(req) {
  return new Promise((resolve, reject) => {
    const chunks = []
    let size = 0
    req.on('data', (chunk) => {
      size += chunk.length
      if (size > maxBodyBytes) {
        reject(Object.assign(new Error('request body is too large'), { status: 413 }))
        req.destroy()
        return
      }
      chunks.push(chunk)
    })
    req.on('end', () => resolve(Buffer.concat(chunks)))
    req.on('error', reject)
  })
}

function isAllowedPath(value) {
  return allowedPaths.some((pattern) => pattern.test(value))
}

async function proxy(req, res) {
  const body = JSON.parse((await readBody(req)).toString('utf8') || '{}')
  const apiKey = typeof body.api_key === 'string' ? body.api_key.trim() : ''
  const requestPath = typeof body.path === 'string' ? body.path : ''
  const method = body.method === 'GET' ? 'GET' : 'POST'
  if (!apiKey) {
    return json(res, 400, { error: 'api_key is required' })
  }
  if (!requestPath.startsWith('/v1/') || !isAllowedPath(requestPath.slice(3))) {
    return json(res, 400, { error: 'unsupported API path' })
  }

  const target = `${upstream}${requestPath}`
  const headers = { Authorization: `Bearer ${apiKey}` }
  if (method === 'POST') headers['Content-Type'] = 'application/json'
  const response = await fetch(target, {
    method,
    headers,
    body: method === 'POST' ? JSON.stringify(body.payload || {}) : undefined,
    signal: AbortSignal.timeout(120000)
  })
  const contentType = response.headers.get('content-type') || 'application/octet-stream'
  const bytes = Buffer.from(await response.arrayBuffer())
  res.writeHead(response.status, {
    'Cache-Control': 'no-store',
    'Content-Type': contentType,
    'Content-Length': bytes.length
  })
  res.end(bytes)
}

const server = createServer(async (req, res) => {
  try {
    const url = new URL(req.url || '/', `http://${req.headers.host || 'localhost'}`)
    if (req.method === 'GET' && url.pathname === '/health') {
      return json(res, 200, { status: 'ok' })
    }
    if (req.method === 'GET' && url.pathname === '/api/config') {
      return json(res, 200, { api_base_url: apiBaseUrl })
    }
    if (req.method === 'POST' && url.pathname === '/api/proxy') {
      return await proxy(req, res)
    }
    if (req.method === 'GET' && (url.pathname === '/' || url.pathname === '/index.html')) {
      const html = await readFile(path.join(root, 'index.html'))
      res.writeHead(200, { 'Cache-Control': 'no-store', 'Content-Type': 'text/html; charset=utf-8' })
      return res.end(html)
    }
    return json(res, 404, { error: 'not found' })
  } catch (error) {
    const status = Number.isInteger(error?.status) ? error.status : 502
    json(res, status, { error: error instanceof Error ? error.message : 'proxy failed' })
  }
})

server.listen(port, '0.0.0.0', () => {
  console.log(`playground sidecar listening on ${port}`)
})
