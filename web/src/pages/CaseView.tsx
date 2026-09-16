import { useEffect, useRef, useState } from 'react'
import { gql } from '../lib/graphqlClient'

type Finding = {
  id: string
  checkName: string
  result: string
  evidence: string
  ruleVersion: string
}

type Job = {
  id: string
  jobType: string
  status: string
  attemptCount: number
  lastError: string | null
}

type Clarification = {
  id: string
  question: string
  answer: string | null
  answeredAt: string | null
  invalidatedAt: string | null
}

type Repair = {
  id: string
  status: string
  reason: string | null
}

type Asset = {
  id: string
  kind: string
  widthPx: number | null
  heightPx: number | null
}

type Order = {
  id: string
  ownerId: string
  productType: string
  declaredWidth: number
  declaredHeight: number
  declaredUnit: string
  intent: string | null
  artworkIsTrimOnly: boolean | null
  caseVersion: number
  artworkStatus: string
  proofStatus: string
  productionStatus: string
  findings: Finding[]
  jobs: Job[]
  clarifications: Clarification[]
  repairs: Repair[]
  assets: Asset[]
}

const ORDER_FIELDS = `
  id ownerId productType declaredWidth declaredHeight declaredUnit intent artworkIsTrimOnly
  caseVersion artworkStatus proofStatus productionStatus
  findings { id checkName result evidence ruleVersion }
  jobs { id jobType status attemptCount lastError }
  clarifications { id question answer answeredAt invalidatedAt }
  repairs { id status reason }
  assets { id kind widthPx heightPx }
`

const RESULT_BADGE: Record<string, string> = {
  PASS: 'badge-ok',
  WARNING: 'badge-warn',
  NEEDS_INPUT: 'badge-warn',
  NEEDS_REVIEW: 'badge-danger',
}

const JOB_BADGE: Record<string, string> = {
  SUCCEEDED: 'badge-ok',
  RUNNING: 'badge-info',
  QUEUED: 'badge-neutral',
  FAILED: 'badge-danger',
}

const REPAIR_BADGE: Record<string, string> = {
  REPAIRED: 'badge-ok',
  REJECTED: 'badge-warn',
}

function artworkStatusBadge(s: string) {
  if (s === 'RESOLVED') return 'badge-ok'
  if (s === 'NEEDS_REVIEW') return 'badge-danger'
  if (s === 'AWAITING_CLARIFICATION') return 'badge-warn'
  return 'badge-neutral'
}

function proofStatusBadge(s: string) {
  return s === 'AWAITING_CUSTOMER_APPROVAL' ? 'badge-info' : 'badge-neutral'
}

function Badge({ text, className }: { text: string; className: string }) {
  return <span className={`badge ${className}`}>{text.replace(/_/g, ' ')}</span>
}

type StepState = 'todo' | 'active' | 'attention' | 'done'

function PipelineTracker({ order }: { order: Order | null }) {
  let orderState: StepState = order ? 'done' : 'active'
  let uploadState: StepState = 'todo'
  let resolveState: StepState = 'todo'
  let proofState: StepState = 'todo'

  if (order) {
    const hasAsset = order.assets.some((a) => a.kind === 'original')
    uploadState = hasAsset ? 'done' : 'active'

    if (hasAsset) {
      if (order.artworkStatus === 'RESOLVED') {
        resolveState = 'done'
      } else if (order.artworkStatus === 'NEEDS_REVIEW') {
        resolveState = 'attention'
      } else if (order.artworkStatus === 'AWAITING_CLARIFICATION') {
        resolveState = 'attention'
      } else if (order.jobs.length > 0) {
        resolveState = 'active'
      }
    }

    if (order.artworkStatus === 'RESOLVED') {
      proofState = order.proofStatus === 'AWAITING_CUSTOMER_APPROVAL' ? 'done' : 'active'
    }
  }

  const steps: { key: string; label: string; state: StepState }[] = [
    { key: 'order', label: 'Order', state: orderState },
    { key: 'upload', label: 'Artwork', state: uploadState },
    { key: 'resolve', label: 'Clarify & repair', state: resolveState },
    { key: 'proof', label: 'Proof', state: proofState },
  ]

  return (
    <div className="pipeline">
      {steps.map((s, i) => (
        <div key={s.key} className={`pipeline-step ${s.state}`}>
          <div className="node">{s.state === 'done' ? '✓' : i + 1}</div>
          <div className="label">{s.label}</div>
        </div>
      ))}
    </div>
  )
}

export default function CaseView() {
  const [ownerId, setOwnerId] = useState('demo-customer')
  const [productType, setProductType] = useState('die-cut-sticker')
  const [width, setWidth] = useState(3)
  const [height, setHeight] = useState(3)
  const [unit, setUnit] = useState<'in' | 'mm'>('in')
  const [intent, setIntent] = useState<'border' | 'full_bleed'>('full_bleed')
  const [customerRequest, setCustomerRequest] = useState('Please check my sticker file before printing.')

  const [order, setOrder] = useState<Order | null>(null)
  const [file, setFile] = useState<File | null>(null)
  const [status, setStatus] = useState<string>('')
  const [busy, setBusy] = useState(false)
  const pollRef = useRef<number | null>(null)

  async function createOrder() {
    setBusy(true)
    setStatus('Creating order…')
    try {
      const data = await gql<{ createOrder: Order }>(
        `mutation($input: CreateOrderInput!) { createOrder(input: $input) { ${ORDER_FIELDS} } }`,
        {
          input: {
            ownerId,
            productType,
            declaredWidth: width,
            declaredHeight: height,
            declaredUnit: unit,
            customerRequest,
            intent,
          },
        },
      )
      setOrder(data.createOrder)
      setStatus(`Order created — ${data.createOrder.id.slice(0, 8)}…`)
    } catch (e) {
      setStatus(`Error: ${(e as Error).message}`)
    } finally {
      setBusy(false)
    }
  }

  async function uploadArtwork() {
    if (!order || !file) return
    setBusy(true)
    setStatus('Requesting upload URL…')
    try {
      const ticket = await gql<{ createUpload: { uploadUrl: string; expiresAt: string } }>(
        `mutation($orderId: ID!, $contentType: String!) {
          createUpload(orderId: $orderId, contentType: $contentType) { uploadUrl expiresAt }
        }`,
        { orderId: order.id, contentType: file.type || 'application/octet-stream' },
      )
      setStatus('Uploading artwork…')
      const res = await fetch(ticket.createUpload.uploadUrl, {
        method: 'POST',
        headers: { 'Content-Type': file.type || 'application/octet-stream' },
        body: file,
      })
      if (!res.ok) throw new Error(await res.text())
      await res.json()
      setStatus('Artwork uploaded.')
      startPolling(order.id)
    } catch (e) {
      setStatus(`Error: ${(e as Error).message}`)
    } finally {
      setBusy(false)
    }
  }

  async function confirmTrim(isTrimOnly: boolean) {
    if (!order) return
    setBusy(true)
    setStatus('Confirming trim…')
    try {
      const data = await gql<{ confirmTrim: Order }>(
        `mutation($orderId: ID!, $artworkIsTrimOnly: Boolean!, $caseVersion: Int!) {
          confirmTrim(orderId: $orderId, artworkIsTrimOnly: $artworkIsTrimOnly, caseVersion: $caseVersion) { ${ORDER_FIELDS} }
        }`,
        { orderId: order.id, artworkIsTrimOnly: isTrimOnly, caseVersion: order.caseVersion },
      )
      setOrder(data.confirmTrim)
      setStatus(isTrimOnly ? 'Confirmed: upload is trim-only (no bleed margin yet).' : 'Confirmed: upload already has bleed.')
    } catch (e) {
      setStatus(`Error: ${(e as Error).message}`)
    } finally {
      setBusy(false)
    }
  }

  // The two buttons below send exactly "yes" or "no" - fixed literals, never
  // free text - because this is the only clarification v1 supports (trim
  // confirmation) and interpretYesNo on the server maps those two words
  // directly to artwork_is_trim_only. A free-text field risks a customer
  // typing something ambiguous ("I think so", "maybe") that a first-word
  // parser could misread; explicit Yes/No controls remove that risk
  // entirely rather than trying to parse around it.
  async function answerClarification(clarificationId: string, answer: 'yes' | 'no') {
    if (!order) return
    setBusy(true)
    setStatus('Submitting answer…')
    try {
      const data = await gql<{ answerClarification: Order }>(
        `mutation($id: ID!, $answer: String!, $caseVersion: Int!) {
          answerClarification(clarificationId: $id, answer: $answer, caseVersion: $caseVersion) { ${ORDER_FIELDS} }
        }`,
        { id: clarificationId, answer, caseVersion: order.caseVersion },
      )
      setOrder(data.answerClarification)
      setStatus('Answer submitted — resolution resumed automatically.')
      startPolling(order.id)
    } catch (e) {
      setStatus(`Error: ${(e as Error).message}`)
    } finally {
      setBusy(false)
    }
  }

  async function startResolution() {
    if (!order) return
    setBusy(true)
    setStatus('Starting resolution…')
    try {
      await gql<{ startResolution: Job }>(
        `mutation($orderId: ID!) { startResolution(orderId: $orderId) { id status jobType } }`,
        { orderId: order.id },
      )
      setStatus('Resolution job enqueued — watching for updates…')
      startPolling(order.id)
    } catch (e) {
      setStatus(`Error: ${(e as Error).message}`)
    } finally {
      setBusy(false)
    }
  }

  function startPolling(orderId: string) {
    if (pollRef.current) window.clearInterval(pollRef.current)
    pollRef.current = window.setInterval(async () => {
      try {
        const data = await gql<{ order: Order }>(`query($id: ID!) { order(id: $id) { ${ORDER_FIELDS} } }`, {
          id: orderId,
        })
        setOrder(data.order)
      } catch (e) {
        setStatus(`Error polling order: ${(e as Error).message}`)
      }
    }, 2000)
  }

  useEffect(() => {
    return () => {
      if (pollRef.current) window.clearInterval(pollRef.current)
    }
  }, [])

  const hasOriginalAsset = !!order?.assets.some((a) => a.kind === 'original')
  const pendingClarification =
    order?.artworkStatus === 'AWAITING_CLARIFICATION'
      ? order.clarifications.find((c) => c.answeredAt === null && c.invalidatedAt === null)
      : undefined

  return (
    <div>
      <PipelineTracker order={order} />

      <div className="card">
        <div className="card-head">
          <h2>
            <span className="step-num">1</span>
            Create order
          </h2>
          {order && <span className="done-mark">Created</span>}
        </div>
        {!order ? (
          <>
            <div className="field-grid">
              <div className="field">
                <label>Owner ID</label>
                <input value={ownerId} onChange={(e) => setOwnerId(e.target.value)} />
              </div>
              <div className="field">
                <label>Product type</label>
                <input value={productType} onChange={(e) => setProductType(e.target.value)} />
              </div>
              <div className="field">
                <label>Dimensions</label>
                <div className="dims-row">
                  <input type="number" value={width} onChange={(e) => setWidth(Number(e.target.value))} placeholder="Width" />
                  <input type="number" value={height} onChange={(e) => setHeight(Number(e.target.value))} placeholder="Height" />
                  <select className="unit" value={unit} onChange={(e) => setUnit(e.target.value as 'in' | 'mm')}>
                    <option value="in">in</option>
                    <option value="mm">mm</option>
                  </select>
                </div>
              </div>
              <div className="field">
                <label>Intent</label>
                <select value={intent} onChange={(e) => setIntent(e.target.value as 'border' | 'full_bleed')}>
                  <option value="full_bleed">full_bleed</option>
                  <option value="border">border</option>
                </select>
              </div>
              <div className="field span-2">
                <label>Customer request</label>
                <textarea value={customerRequest} onChange={(e) => setCustomerRequest(e.target.value)} rows={2} />
              </div>
            </div>
            <button className="btn btn-primary" onClick={createOrder} disabled={busy}>
              Create order
            </button>
          </>
        ) : (
          <div className="meta-row">
            <span>
              Order <span className="mono">{order.id}</span>
            </span>
            <span>
              {order.declaredWidth}×{order.declaredHeight} {order.declaredUnit}
            </span>
            <span>intent: {order.intent}</span>
          </div>
        )}
      </div>

      {order && (
        <div className="card">
          <div className="card-head">
            <h2>
              <span className="step-num">2</span>
              Upload artwork
            </h2>
            {hasOriginalAsset && <span className="done-mark">Uploaded</span>}
          </div>
          <div className="file-input">
            <input type="file" accept="image/png,image/jpeg" onChange={(e) => setFile(e.target.files?.[0] ?? null)} />
            <button className="btn btn-secondary" onClick={uploadArtwork} disabled={!file || busy}>
              Upload
            </button>
          </div>
        </div>
      )}

      {order && order.intent === 'full_bleed' && (
        <div className="card">
          <div className="card-head">
            <h2>
              <span className="step-num">3</span>
              Confirm trim
            </h2>
          </div>
          <p className="hint">Full-bleed intent needs this before resolution/bleed can be measured, rather than guessed.</p>
          <div className="meta-row" style={{ marginBottom: '0.9rem' }}>
            Current:{' '}
            <Badge
              text={order.artworkIsTrimOnly === null ? 'unconfirmed' : order.artworkIsTrimOnly ? 'trim-only' : 'bleed included'}
              className={order.artworkIsTrimOnly === null ? 'badge-neutral' : 'badge-info'}
            />
          </div>
          <div className="btn-row">
            <button className="btn btn-secondary" onClick={() => confirmTrim(true)} disabled={busy}>
              This upload is trim-only
            </button>
            <button className="btn btn-secondary" onClick={() => confirmTrim(false)} disabled={busy}>
              This upload already has bleed
            </button>
          </div>
        </div>
      )}

      {order && (
        <div className="card">
          <div className="card-head">
            <h2>
              <span className="step-num">4</span>
              Start resolution
            </h2>
          </div>
          <button className="btn btn-primary" onClick={startResolution} disabled={busy}>
            Start resolution
          </button>
        </div>
      )}

      {pendingClarification && (
        <div className="card attention">
          <div className="card-head">
            <h2>💬 The agent has a question</h2>
          </div>
          <p className="clarification-question">{pendingClarification.question}</p>
          <div className="btn-row">
            <button className="btn btn-primary" onClick={() => answerClarification(pendingClarification.id, 'yes')} disabled={busy}>
              Yes
            </button>
            <button className="btn btn-secondary" onClick={() => answerClarification(pendingClarification.id, 'no')} disabled={busy}>
              No
            </button>
          </div>
        </div>
      )}

      {status && (
        <div className="status-line">
          {busy && <span className="spinner" />}
          {status}
        </div>
      )}

      {order && (
        <div className="card">
          <div className="card-head">
            <h2>Case status</h2>
          </div>

          <div className="status-grid">
            <div className="status-tile">
              <span className="status-label">Artwork</span>
              <Badge text={order.artworkStatus} className={artworkStatusBadge(order.artworkStatus)} />
            </div>
            <div className="status-tile">
              <span className="status-label">Proof</span>
              <Badge text={order.proofStatus} className={proofStatusBadge(order.proofStatus)} />
            </div>
            <div className="status-tile">
              <span className="status-label">Production</span>
              <Badge text={order.productionStatus} className="badge-neutral" />
            </div>
          </div>

          <div className="subsection">
            <h3>Jobs</h3>
            {order.jobs.length === 0 ? (
              <div className="empty-note">None yet.</div>
            ) : (
              <div className="entry-list">
                {order.jobs.map((j) => (
                  <div className="entry-row" key={j.id}>
                    <div className="entry-main">
                      <span className="entry-name">{j.jobType}</span>
                      <span className="entry-detail">
                        attempt {j.attemptCount}
                        {j.lastError ? ` — ${j.lastError}` : ''}
                      </span>
                    </div>
                    <Badge text={j.status} className={JOB_BADGE[j.status] ?? 'badge-neutral'} />
                  </div>
                ))}
              </div>
            )}
          </div>

          <div className="subsection">
            <h3>Findings</h3>
            {order.findings.length === 0 ? (
              <div className="empty-note">None yet.</div>
            ) : (
              <div className="entry-list">
                {order.findings.map((f) => (
                  <div className="entry-row" key={f.id}>
                    <div className="entry-main">
                      <span className="entry-name">{f.checkName}</span>
                      <span className="entry-detail">rule {f.ruleVersion}</span>
                    </div>
                    <Badge text={f.result} className={RESULT_BADGE[f.result] ?? 'badge-neutral'} />
                  </div>
                ))}
              </div>
            )}
          </div>

          {order.repairs.length > 0 && (
            <div className="subsection">
              <h3>Repairs</h3>
              <div className="entry-list">
                {order.repairs.map((r) => (
                  <div className="entry-row" key={r.id}>
                    <div className="entry-main">
                      <span className="entry-name">uniform-background extension</span>
                      {r.reason && <span className="entry-detail">{r.reason}</span>}
                    </div>
                    <Badge text={r.status} className={REPAIR_BADGE[r.status] ?? 'badge-neutral'} />
                  </div>
                ))}
              </div>
            </div>
          )}

          <div className="subsection">
            <h3>Clarifications</h3>
            {order.clarifications.length === 0 ? (
              <div className="empty-note">None yet.</div>
            ) : (
              <div className="entry-list">
                {order.clarifications.map((c) => (
                  <div className="entry-row" key={c.id}>
                    <div className="entry-main">
                      <span className="entry-detail">{c.question}</span>
                    </div>
                    {c.answer ? (
                      <Badge text={c.answer} className="badge-info" />
                    ) : c.invalidatedAt ? (
                      <Badge text="invalidated" className="badge-neutral" />
                    ) : (
                      <Badge text="awaiting" className="badge-warn" />
                    )}
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  )
}
