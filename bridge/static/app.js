(() => {
  'use strict'

  // The bridge is served from the same origin as Sub2API. Never accept a JWT
  // from the URL: URLs leak through browser history, logs and referrers.
  const token = String(window.localStorage.getItem('auth_token') || '').trim()
  let user = null
  let skus = []
  let channels = []
  let selectedSku = null
  let pendingOrder = null
  let channelLoadError = ''
  let activeOrder = null
  let ordersCache = []
  const expandedOrders = new Set()
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
    paymentMethods: $('payment-methods'),
    refreshPayment: $('refresh-payment'), consoleLink: $('console-link'), supportLink: $('support-link'),
    footerSupport: $('footer-support'), footerSupportHint: $('footer-support-hint'),
    orders: $('orders'), ordersSummary: $('orders-summary'), orderSearch: $('order-search'),
    orderStatus: $('order-status'), reloadOrders: $('reload-orders'),
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
    ui.submit.disabled = busy || !selectedSku
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
    if (!pendingOrder) return
    if (!channels.length) {
      const title = channelLoadError ? '支付方式加载失败' : '支付通道维护中'
      const detail = channelLoadError || '账号登录和充值记录不受影响。支付通道恢复后，刷新本页即可充值。'
      ui.channels.innerHTML = `<div class="notice info"><strong>${escapeHTML(title)}</strong><p>${escapeHTML(detail)}</p><button class="text-button" type="button" data-retry-channels>重新获取支付方式</button>${supportMarkup()}</div>`
      ui.channels.querySelector('[data-retry-channels]').addEventListener('click', refreshPaymentMethods)
      return
    }
    ui.channels.innerHTML = channels.map((channel) => `
      <button type="button" class="channel" data-channel="${channel.id}"${busy ? ' disabled' : ''}>
        <span class="channel-icon">${channelIcon(channel)}</span>
        <span class="channel-copy">
          <span class="channel-name">${escapeHTML(channel.name)}</span>
          <span class="channel-type">${escapeHTML(channel.interaction_mode || channel.channel_type || '在线支付')} · 点击继续</span>
        </span>
      </button>`).join('')
    ui.channels.querySelectorAll('[data-channel]').forEach((button) => {
      button.addEventListener('click', () => payWithChannel(pendingOrder, Number(button.dataset.channel)))
    })
  }

  function renderOrders(orders) {
    ordersCache = Array.isArray(orders) ? orders : []
    const query = String(ui.orderSearch.value || '').trim().toLowerCase()
    const filter = ui.orderStatus.value
    const filtered = ordersCache.filter((order) => {
      if (query && !String(order.order_no || '').toLowerCase().includes(query)) return false
      return statusMatchesFilter(order.status, filter)
    })
    if (!filtered.length) {
      ui.ordersSummary.textContent = ordersCache.length ? `共 ${ordersCache.length} 条，当前筛选无结果` : ''
      ui.orders.innerHTML = ordersCache.length
        ? '<span class="muted">没有符合筛选条件的充值记录，请调整搜索或状态。</span>'
        : '<span class="muted">还没有充值记录。</span>'
      return
    }
    ui.ordersSummary.textContent = `显示 ${filtered.length} / ${ordersCache.length} 条，最近 30 条充值记录`
    ui.orders.innerHTML = filtered.map((order) => {
      const [label, kind] = statusMeta(order.status)
      const action = orderAction(order.status)
      return `<article class="order-item">
        <div class="order-row">
          <div class="order-main">
            <div class="order-id">${escapeHTML(order.order_no)}</div>
            <div class="order-date">${escapeHTML(new Date(order.created_at).toLocaleString('zh-CN'))}</div>
          </div>
          <div class="order-amount">${escapeHTML(moneyLabel(order.order_amount, order.currency))}</div>
          <span class="pill ${kind}">${escapeHTML(label)}</span>
          <button class="text-button order-action" type="button" data-order-action="${escapeHTML(order.order_no)}">${escapeHTML(action)}</button>
        </div>
        <details class="order-detail"${expandedOrders.has(order.order_no) ? ' open' : ''} data-order-detail="${escapeHTML(order.order_no)}">
          <summary>订单详情</summary>
          <dl>
            <div><dt>套餐</dt><dd>${escapeHTML(order.sku_key)}</dd></div>
            <div><dt>数量</dt><dd>${escapeHTML(order.quantity)}</dd></div>
            <div><dt>到账额度</dt><dd>${escapeHTML(creditLabel(order.credit_amount))}</dd></div>
            <div><dt>创建时间</dt><dd>${escapeHTML(new Date(order.created_at).toLocaleString('zh-CN'))}</dd></div>
            ${order.paid_at ? `<div><dt>支付确认</dt><dd>${escapeHTML(new Date(order.paid_at).toLocaleString('zh-CN'))}</dd></div>` : ''}
            ${order.credited_at ? `<div><dt>到账时间</dt><dd>${escapeHTML(new Date(order.credited_at).toLocaleString('zh-CN'))}</dd></div>` : ''}
          </dl>
        </details>
      </article>`
    }).join('')
    ui.orders.querySelectorAll('[data-order-action]').forEach((button) => {
      button.addEventListener('click', () => resumeOrder(button.dataset.orderAction))
    })
    ui.orders.querySelectorAll('[data-order-detail]').forEach((details) => {
      details.addEventListener('toggle', () => {
        if (details.open) expandedOrders.add(details.dataset.orderDetail)
        else expandedOrders.delete(details.dataset.orderDetail)
      })
    })
  }

  function statusMatchesFilter(status, filter) {
    if (filter === 'all') return true
    if (filter === 'unpaid') return status === 'created' || status === 'pending_payment'
    if (filter === 'processing') return status === 'paid' || status === 'crediting' || status === 'credit_failed'
    if (filter === 'credited') return status === 'credited'
    if (filter === 'closed') return status === 'canceled' || status === 'refunded'
    return true
  }

  function orderAction(status) {
    if (status === 'created' || status === 'pending_payment') return '继续支付'
    if (status === 'paid' || status === 'crediting' || status === 'credit_failed') return '查看状态'
    return '查看详情'
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

  async function loadPaymentChannels(order) {
    const data = await api(`api/payment-channels?order_no=${encodeURIComponent(order.order_no)}`)
    channels = data.channels || []
    channelLoadError = ''
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

  function paymentRequired(order) {
    return (order.status === 'created' || order.status === 'pending_payment') && !order.pay_url && !order.qr_code
  }

  async function requestPayment(order, channelId) {
    const data = await api(`api/orders/${encodeURIComponent(order.order_no)}/pay`, {
      method: 'POST',
      body: JSON.stringify({ channel_id: channelId })
    })
    return data.order
  }

  function hidePaymentMethods() {
    pendingOrder = null
    channels = []
    channelLoadError = ''
    ui.channels.innerHTML = ''
    ui.paymentMethods.classList.add('hidden')
  }

  function showAwaitingPayment(order, message = '') {
    window.clearTimeout(pollTimer)
    pendingOrder = order
    activeOrder = order
    ui.payment.classList.remove('hidden')
    ui.paymentMethods.classList.remove('hidden')
    ui.paymentStatus.textContent = '待支付'
    ui.paymentStatus.className = 'status pending'
    ui.paymentMessage.textContent = message || '充值档位已锁定，请选择下方任意可用支付方式完成付款。'
    ui.paymentMeta.textContent = `${order.order_no} · 应付 ${moneyLabel(order.order_amount, order.currency)}`
    ui.qrImage.removeAttribute('src')
    ui.qrWrap.classList.add('hidden')
    ui.payLink.removeAttribute('href')
    ui.payLink.classList.add('hidden')
    ui.consoleLink.classList.add('hidden')
    ui.refreshPayment.classList.add('hidden')
    ui.supportLink.classList.toggle('hidden', !supportUrl)
    renderChannels()
    ui.payment.scrollIntoView({ behavior: 'smooth', block: 'center' })
  }

  async function preparePayment(order) {
    channels = []
    channelLoadError = ''
    try {
      await loadPaymentChannels(order)
    } catch (error) {
      channelLoadError = error.message
    }
    if (!channelLoadError && channels.length === 1) {
      try {
        const paid = await requestPayment(order, channels[0].id)
        showOrder(paid)
        return
      } catch (error) {
        channelLoadError = error.message
        toast(error.message)
      }
    }
    showAwaitingPayment(order, channelLoadError)
  }

  async function continueOrder(order) {
    if (!paymentRequired(order)) {
      showOrder(order)
      return
    }
    const previousChannelId = Number(order.channel_id || 0)
    if (previousChannelId > 0) {
      try {
        showOrder(await requestPayment(order, previousChannelId))
        return
      } catch (error) {
        toast(error.message)
      }
    }
    await preparePayment(order)
  }

  async function payWithChannel(order, channelId) {
    if (busy || !order || !channelId) return
    busy = true
    updateSubmitState()
    ui.channels.querySelectorAll('[data-channel]').forEach((button) => { button.disabled = true })
    let failure = ''
    try {
      showOrder(await requestPayment(order, channelId))
      await loadOrders()
    } catch (error) {
      failure = error.message
      toast(error.message)
    } finally {
      busy = false
      updateSubmitState()
    }
    if (failure && pendingOrder) showAwaitingPayment(pendingOrder, failure)
  }

  async function refreshPaymentMethods() {
    if (busy || !pendingOrder) return
    const order = pendingOrder
    busy = true
    updateSubmitState()
    channels = []
    channelLoadError = ''
    try {
      await loadPaymentChannels(order)
      if (channels.length === 1) {
        showOrder(await requestPayment(order, channels[0].id))
        return
      }
      showAwaitingPayment(order)
    } catch (error) {
      channelLoadError = error.message
      showAwaitingPayment(order, error.message)
    } finally {
      busy = false
      updateSubmitState()
      if (!ui.paymentMethods.classList.contains('hidden')) renderChannels()
    }
  }

  async function createPayment() {
    if (busy || !selectedSku) return
    busy = true
    updateSubmitState()
    ui.submit.textContent = '正在创建订单…'
    try {
      const quantity = Number(ui.quantity.value)
      const created = await api('api/orders', {
        method: 'POST',
        headers: { 'Idempotency-Key': newIdempotencyKey() },
        body: JSON.stringify({ sku_key: selectedSku.key, quantity })
      })
      await continueOrder(created.order)
      await loadOrders()
    } catch (error) {
      toast(error.message)
    } finally {
      busy = false
      ui.submit.textContent = '立即充值'
      updateSubmitState()
      if (!ui.paymentMethods.classList.contains('hidden')) renderChannels()
    }
  }

  function showOrder(order) {
    hidePaymentMethods()
    activeOrder = order
    ui.payment.classList.remove('hidden')
    renderPayment(order)
    ui.payment.scrollIntoView({ behavior: 'smooth', block: 'center' })
    schedulePoll()
  }

  async function resumeOrder(orderNo) {
    if (busy || !orderNo) return
    busy = true
    try {
      const detail = await api(`api/orders/${encodeURIComponent(orderNo)}`)
      await continueOrder(detail.order)
      await loadOrders()
    } catch (error) {
      toast(error.message)
    } finally {
      busy = false
      updateSubmitState()
      if (!ui.paymentMethods.classList.contains('hidden')) renderChannels()
    }
  }

  function renderPayment(order) {
    const [label, kind] = statusMeta(order.status)
    ui.paymentStatus.textContent = label
    ui.paymentStatus.className = `status ${kind}`

    const payable = order.status === 'created' || order.status === 'pending_payment'
    const qr = String(order.qr_code || '')
    if (payable && qr && (/^data:image\//.test(qr) || /^https?:\/\//.test(qr))) {
      ui.qrImage.src = qr
      ui.qrWrap.classList.remove('hidden')
    } else {
      ui.qrImage.removeAttribute('src')
      ui.qrWrap.classList.add('hidden')
    }

    if (payable && /^https?:\/\//.test(String(order.pay_url || ''))) {
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
  ui.orderSearch.addEventListener('input', () => renderOrders(ordersCache))
  ui.orderStatus.addEventListener('change', () => renderOrders(ordersCache))

  Promise.all([loadCatalog(), loadOrders()]).catch((error) => {
    ui.loginError.classList.remove('hidden')
    ui.loginError.querySelector('p').textContent = error.message
    ui.recharge.classList.add('hidden')
  })
})()
