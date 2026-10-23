(() => {
  'use strict'

  // The bridge is served from the same origin as Sub2API. Never accept a JWT
  // from the URL: URLs leak through browser history, logs and referrers.
  const token = String(window.localStorage.getItem('auth_token') || '').trim()
  let user = null
  let skus = []
  let channels = []
  let selectedSku = null
  let selectedChannelId = 0
  let activeOrder = null
  let supportUrl = ''
  let supportLabel = '联系客服'
  let pollTimer = null
  let busy = false

  const $ = (id) => document.getElementById(id)
  const ui = {
    account: $('account'), loginError: $('login-error'), recharge: $('recharge'),
    skus: $('skus'), channels: $('channels'), quantity: $('quantity'), quantityRow: $('quantity-row'),
    quantityHint: $('quantity-hint'), totalPrice: $('total-price'), submit: $('submit'),
    payment: $('payment'), paymentStatus: $('payment-status'), paymentMessage: $('payment-message'),
    payLink: $('pay-link'), paymentMeta: $('payment-meta'), qrWrap: $('qr-wrap'), qrImage: $('qr-image'),
    refreshPayment: $('refresh-payment'), consoleLink: $('console-link'), supportLink: $('support-link'),
    footerSupport: $('footer-support'), footerSupportHint: $('footer-support-hint'),
    orders: $('orders'), reloadOrders: $('reload-orders'),
    toast: $('toast')
  }

  if (!token) {
    ui.loginError.classList.remove('hidden')
    return
  }

  async function api(path, options = {}) {
    const headers = new Headers(options.headers || {})
    headers.set('Authorization', `Bearer ${token}`)
    headers.set('Accept', 'application/json')
    if (options.body) headers.set('Content-Type', 'application/json')
    const response = await fetch(path, { ...options, headers, cache: 'no-store' })
    let payload = null
    try { payload = await response.json() } catch (_) {}
    if (!response.ok) {
      const message = payload && payload.message ? payload.message : `请求失败 (${response.status})`
      throw new Error(message)
    }
    return payload
  }

  function escapeHTML(value) {
    return String(value ?? '').replace(/[&<>"']/g, (char) => ({
      '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
    })[char])
  }

  function moneyLabel(amount, currency) {
    const value = Number(amount)
    if (!Number.isFinite(value)) return `${currency || ''} ${amount}`
    const formatted = value.toFixed(2).replace(/\.00$/, '')
    if (currency === 'CNY') return `¥${formatted}`
    if (currency === 'USD') return `$${formatted}`
    return `${currency || ''} ${formatted}`
  }

  function creditLabel(value) {
    return `${Number(value).toFixed(2).replace(/\.00$/, '')}`
  }

  function toast(message) {
    ui.toast.textContent = message
    ui.toast.classList.remove('hidden')
    window.clearTimeout(toast.timer)
    toast.timer = window.setTimeout(() => ui.toast.classList.add('hidden'), 4200)
  }

  function statusMeta(status) {
    return {
      created: ['待提交', 'pending'],
      pending_payment: ['等待支付', 'pending'],
      paid: ['支付已确认，正在到账', 'paid'],
      crediting: ['正在到账', 'paid'],
      credited: ['充值成功', 'success'],
      credit_failed: ['到账重试中', 'error'],
      canceled: ['已取消', ''],
      refunded: ['已退款', 'error']
    }[status] || [status || '未知', '']
  }

  function renderAccount() {
    ui.account.innerHTML = `
      <div class="account-line">
        <div>
          <div class="muted">${escapeHTML(user.email || user.username || `用户 ${user.id}`)}</div>
          <div class="balance">${escapeHTML(creditLabel(user.balance))}</div>
        </div>
        <span class="safe-badge">已安全登录</span>
      </div>`
  }

  function renderSkus() {
    if (!skus.length) {
      resetSkuSelection()
      ui.skus.innerHTML = '<div class="notice info"><strong>暂未配置充值套餐</strong><p>账号已连接成功。管理员配置商品后，刷新本页即可充值。</p></div>'
      ui.quantityRow.classList.add('hidden')
      updateTotal()
      updateSubmitState()
      return
    }
    ui.quantityRow.classList.remove('hidden')
    ui.skus.innerHTML = skus.map((sku) => `
      <button type="button" class="sku${selectedSku && selectedSku.key === sku.key ? ' selected' : ''}" data-sku="${escapeHTML(sku.key)}">
        <span class="sku-top">
          <span class="sku-name">${escapeHTML(sku.display_name)}</span>
          ${sku.recommended ? '<span class="recommended">推荐</span>' : ''}
        </span>
        <span class="sku-price">${escapeHTML(moneyLabel(Number(sku.price_cents) / 100, sku.currency))}</span>
        <span class="sku-credit">到账 ${escapeHTML(creditLabel(Number(sku.credit_cents) / 100))} ${escapeHTML(sku.credit_unit || '余额')}</span>
      </button>`).join('')
    ui.skus.querySelectorAll('[data-sku]').forEach((button) => {
      button.addEventListener('click', () => selectSku(button.dataset.sku))
    })
    updateQuantityBounds()
    updateSubmitState()
  }

  function resetSkuSelection() {
    selectedSku = null
    ui.quantity.value = '1'
    ui.quantity.min = '1'
    ui.quantity.max = '1'
  }

  function selectSku(key) {
    selectedSku = skus.find((sku) => sku.key === key) || null
    ui.skus.querySelectorAll('[data-sku]').forEach((node) => {
      node.classList.toggle('selected', selectedSku && node.dataset.sku === selectedSku.key)
    })
    updateQuantityBounds()
    updateTotal()
    updateSubmitState()
  }

  function updateQuantityBounds() {
    if (!selectedSku) return
    ui.quantity.min = String(selectedSku.min_quantity || 1)
    ui.quantity.max = String(selectedSku.max_quantity || 1)
    const value = Math.min(Math.max(Number(ui.quantity.value || 1), Number(ui.quantity.min)), Number(ui.quantity.max))
    ui.quantity.value = String(value)
    ui.quantityHint.textContent = `每份 ${selectedSku.min_quantity}–${selectedSku.max_quantity} 份`
    updateTotal()
  }

  function updateTotal() {
    if (!selectedSku) {
      ui.totalPrice.textContent = '—'
      return
    }
    const quantity = Number(ui.quantity.value || 1)
    ui.totalPrice.textContent = moneyLabel(Number(selectedSku.price_cents) / 100 * quantity, selectedSku.currency)
  }

  function updateSubmitState() {
    ui.submit.disabled = busy || !selectedSku || !channels.length || !selectedChannelId
  }

  function supportMarkup() {
    if (!supportUrl) return ''
    return `<a class="text-button support-link" href="${escapeHTML(supportUrl)}" target="_blank" rel="noopener noreferrer">${escapeHTML(supportLabel)}</a>`
  }

  function renderSupport() {
    const hasSupport = Boolean(supportUrl)
    for (const link of [ui.supportLink, ui.footerSupport]) {
      if (!link) continue
      if (hasSupport) {
        link.href = supportUrl
        link.textContent = supportLabel
        link.classList.remove('hidden')
      } else {
        link.removeAttribute('href')
        link.classList.add('hidden')
      }
    }
    if (ui.footerSupportHint) ui.footerSupportHint.classList.toggle('hidden', hasSupport)
  }

  function channelIcon(channel) {
    const text = `${channel.name} ${channel.provider_type} ${channel.channel_type}`.toLowerCase()
    if (text.includes('alipay') || text.includes('支付宝')) return '支'
    if (text.includes('wechat') || text.includes('微信')) return '微'
    return '付'
  }

  function renderChannels() {
    if (!channels.length) {
      selectedChannelId = 0
      ui.channels.innerHTML = `<div class="notice info"><strong>支付通道维护中</strong><p>账号登录和充值记录不受影响。支付通道恢复后，刷新本页即可充值。</p>${supportMarkup()}</div>`
      updateSubmitState()
      return
    }
    if (!channels.some((channel) => channel.id === selectedChannelId)) selectedChannelId = channels[0].id
    ui.channels.innerHTML = channels.map((channel) => `
      <button type="button" class="channel${channel.id === selectedChannelId ? ' selected' : ''}" data-channel="${channel.id}">
        <span class="channel-icon">${channelIcon(channel)}</span>
        <span class="channel-copy">
          <span class="channel-name">${escapeHTML(channel.name)}</span>
          <span class="channel-type">${escapeHTML(channel.interaction_mode || channel.channel_type || '在线支付')}</span>
        </span>
      </button>`).join('')
    ui.channels.querySelectorAll('[data-channel]').forEach((button) => {
      button.addEventListener('click', () => {
        selectedChannelId = Number(button.dataset.channel)
        renderChannels()
      })
    })
    updateSubmitState()
  }

  function renderOrders(orders) {
    if (!orders || !orders.length) {
      ui.orders.innerHTML = '<span class="muted">还没有充值记录。</span>'
      return
    }
    ui.orders.innerHTML = orders.map((order) => {
      const [label, kind] = statusMeta(order.status)
      return `<div class="order-row">
        <div>
          <div class="order-id">${escapeHTML(order.order_no)}</div>
          <div class="order-date">${escapeHTML(new Date(order.created_at).toLocaleString('zh-CN'))}</div>
        </div>
        <div class="order-amount">${escapeHTML(moneyLabel(order.order_amount, order.currency))}</div>
        <span class="pill ${kind}">${escapeHTML(label)}</span>
      </div>`
    }).join('')
  }

  async function loadCatalog() {
    const data = await api('api/catalog')
    user = data.user
    skus = data.skus || []
    supportUrl = String(data.support_url || '')
    supportLabel = String(data.support_label || '联系客服')
    renderSupport()
    selectedSku = skus.find((sku) => sku.recommended) || skus[0]
    renderAccount()
    renderSkus()
    updateTotal()
    ui.recharge.classList.remove('hidden')
  }

  async function loadChannels() {
    try {
      const data = await api('api/payment-channels')
      channels = data.channels || []
      renderChannels()
    } catch (error) {
      channels = []
      selectedChannelId = 0
      ui.channels.innerHTML = `<div class="notice info"><strong>支付通道暂时不可用</strong><p>${escapeHTML(error.message)}</p>${supportMarkup()}</div>`
      updateSubmitState()
    }
  }

  async function loadOrders() {
    try {
      const data = await api('api/orders')
      renderOrders(data.orders || [])
    } catch (error) {
      ui.orders.innerHTML = `<span class="muted">${escapeHTML(error.message)}</span>`
    }
  }

  function newIdempotencyKey() {
    if (window.crypto && crypto.randomUUID) return crypto.randomUUID()
    return `ui-${Date.now()}-${Math.random().toString(16).slice(2)}`
  }

  async function createPayment() {
    if (busy || !selectedSku || !selectedChannelId) return
    busy = true
    updateSubmitState()
    ui.submit.textContent = '正在创建订单…'
    try {
      const quantity = Number(ui.quantity.value)
      const created = await api('api/orders', {
        method: 'POST',
        headers: { 'Idempotency-Key': newIdempotencyKey() },
        body: JSON.stringify({ sku_key: selectedSku.key, quantity, channel_id: selectedChannelId })
      })
      let order = created.order
      if (order.status !== 'credited' && order.status !== 'canceled' && order.status !== 'refunded' && !order.pay_url && !order.qr_code) {
        const paid = await api(`api/orders/${encodeURIComponent(order.order_no)}/pay`, {
          method: 'POST',
          body: JSON.stringify({ channel_id: selectedChannelId })
        })
        order = paid.order
      }
      showOrder(order)
      await loadOrders()
    } catch (error) {
      toast(error.message)
    } finally {
      busy = false
      ui.submit.textContent = '立即充值'
      updateSubmitState()
    }
  }

  function showOrder(order) {
    activeOrder = order
    ui.payment.classList.remove('hidden')
    renderPayment(order)
    ui.payment.scrollIntoView({ behavior: 'smooth', block: 'center' })
    schedulePoll()
  }

  function renderPayment(order) {
    const [label, kind] = statusMeta(order.status)
    ui.paymentStatus.textContent = label
    ui.paymentStatus.className = `status ${kind}`

    const qr = String(order.qr_code || '')
    if (qr && (/^data:image\//.test(qr) || /^https?:\/\//.test(qr))) {
      ui.qrImage.src = qr
      ui.qrWrap.classList.remove('hidden')
    } else {
      ui.qrImage.removeAttribute('src')
      ui.qrWrap.classList.add('hidden')
    }

    if (order.pay_url) {
      ui.payLink.href = order.pay_url
      ui.payLink.classList.remove('hidden')
    } else {
      ui.payLink.removeAttribute('href')
      ui.payLink.classList.add('hidden')
    }

    if (order.status === 'credited') {
      ui.paymentMessage.textContent = '充值已到账，可以返回 XDD API 继续使用。'
      ui.consoleLink.classList.remove('hidden')
      ui.supportLink.classList.add('hidden')
      ui.refreshPayment.classList.add('hidden')
      window.clearTimeout(pollTimer)
      loadCatalog().catch(() => {})
    } else if (order.status === 'credit_failed') {
      ui.paymentMessage.textContent = order.message || '支付已确认，到账暂时失败，系统会自动重试。'
      ui.consoleLink.classList.add('hidden')
      ui.supportLink.classList.toggle('hidden', !supportUrl)
      ui.refreshPayment.classList.remove('hidden')
      ui.refreshPayment.textContent = '立即查询到账状态'
    } else if (order.status === 'canceled' || order.status === 'refunded') {
      ui.paymentMessage.textContent = '该订单已关闭。如已付款，请联系管理员处理。'
      ui.consoleLink.classList.add('hidden')
      ui.supportLink.classList.toggle('hidden', !supportUrl)
      ui.refreshPayment.classList.add('hidden')
      window.clearTimeout(pollTimer)
    } else {
      ui.paymentMessage.textContent = '请在支付页面完成付款。支付成功后本页面会自动更新，无需重复提交。'
      ui.consoleLink.classList.add('hidden')
      ui.supportLink.classList.add('hidden')
      ui.refreshPayment.classList.remove('hidden')
      ui.refreshPayment.textContent = '我已支付，立即查询'
    }
    ui.paymentMeta.textContent = `${order.order_no} · 应付 ${moneyLabel(order.order_amount, order.currency)}`
  }

  async function refreshPayment() {
    if (!activeOrder || busy) return
    busy = true
    ui.refreshPayment.disabled = true
    try {
      const data = await api(`api/orders/${encodeURIComponent(activeOrder.order_no)}/refresh`, { method: 'POST' })
      activeOrder = data.order
      renderPayment(activeOrder)
      await loadOrders()
    } catch (error) {
      toast(error.message)
    } finally {
      busy = false
      ui.refreshPayment.disabled = false
    }
  }

  function schedulePoll() {
    window.clearTimeout(pollTimer)
    if (!activeOrder) return
    if (['credited', 'canceled', 'refunded'].includes(activeOrder.status)) return
    pollTimer = window.setTimeout(async () => {
      if (document.hidden) {
        schedulePoll()
        return
      }
      await refreshPayment()
      if (activeOrder && !['credited', 'canceled', 'refunded'].includes(activeOrder.status)) schedulePoll()
    }, 3000)
  }

  $('qty-minus').addEventListener('click', () => {
    ui.quantity.value = String(Math.max(Number(ui.quantity.min || 1), Number(ui.quantity.value) - 1))
    updateTotal()
  })
  $('qty-plus').addEventListener('click', () => {
    ui.quantity.value = String(Math.min(Number(ui.quantity.max || 1), Number(ui.quantity.value) + 1))
    updateTotal()
  })
  ui.quantity.addEventListener('input', updateQuantityBounds)
  ui.submit.addEventListener('click', createPayment)
  ui.refreshPayment.addEventListener('click', refreshPayment)
  ui.reloadOrders.addEventListener('click', loadOrders)

  Promise.all([loadCatalog(), loadChannels(), loadOrders()]).catch((error) => {
    ui.loginError.classList.remove('hidden')
    ui.loginError.querySelector('p').textContent = error.message
    ui.recharge.classList.add('hidden')
  })
})()
