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
}

const ORDER_FIELDS = `
  id ownerId productType declaredWidth declaredHeight declaredUnit intent artworkIsTrimOnly
  caseVersion artworkStatus proofStatus productionStatus
  findings { id checkName result evidence ruleVersion }
  jobs { id jobType status attemptCount lastError }
  clarifications { id question answer answeredAt invalidatedAt }
`

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
  const pollRef = useRef<number | null>(null)

  async function createOrder() {
    setStatus('Creating order...')
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
      setStatus(`Order ${data.createOrder.id} created.`)
    } catch (e) {
      setStatus(`Error: ${(e as Error).message}`)
    }
  }

  async function uploadArtwork() {
    if (!order || !file) return
    setStatus('Requesting upload URL...')
    try {
      const ticket = await gql<{ createUpload: { uploadUrl: string; expiresAt: string } }>(
        `mutation($orderId: ID!, $contentType: String!) {
          createUpload(orderId: $orderId, contentType: $contentType) { uploadUrl expiresAt }
        }`,
        { orderId: order.id, contentType: file.type || 'application/octet-stream' },
      )
      setStatus('Uploading artwork...')
      const res = await fetch(ticket.createUpload.uploadUrl, {
        method: 'POST',
        headers: { 'Content-Type': file.type || 'application/octet-stream' },
        body: file,
      })
      if (!res.ok) throw new Error(await res.text())
      const body = await res.json()
      setStatus(`Artwork uploaded as asset ${body.assetId}.`)
    } catch (e) {
      setStatus(`Error: ${(e as Error).message}`)
    }
  }

  async function confirmTrim(isTrimOnly: boolean) {
    if (!order) return
    setStatus('Confirming trim...')
    try {
      const data = await gql<{ confirmTrim: Order }>(
        `mutation($orderId: ID!, $artworkIsTrimOnly: Boolean!, $caseVersion: Int!) {
          confirmTrim(orderId: $orderId, artworkIsTrimOnly: $artworkIsTrimOnly, caseVersion: $caseVersion) { ${ORDER_FIELDS} }
        }`,
        { orderId: order.id, artworkIsTrimOnly: isTrimOnly, caseVersion: order.caseVersion },
      )
      setOrder(data.confirmTrim)
      setStatus(isTrimOnly ? 'Confirmed: upload is trim-only (no bleed margin yet).' : 'Confirmed: upload is not trim-only.')
    } catch (e) {
      setStatus(`Error: ${(e as Error).message}`)
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
    setStatus('Submitting answer...')
    try {
      const data = await gql<{ answerClarification: Order }>(
        `mutation($id: ID!, $answer: String!, $caseVersion: Int!) {
          answerClarification(clarificationId: $id, answer: $answer, caseVersion: $caseVersion) { ${ORDER_FIELDS} }
        }`,
        { id: clarificationId, answer, caseVersion: order.caseVersion },
      )
      setOrder(data.answerClarification)
      setStatus('Answer submitted - resolution resumed automatically.')
      startPolling(order.id)
    } catch (e) {
      setStatus(`Error: ${(e as Error).message}`)
    }
  }

  async function startResolution() {
    if (!order) return
    setStatus('Starting resolution...')
    try {
      await gql<{ startResolution: Job }>(
        `mutation($orderId: ID!) { startResolution(orderId: $orderId) { id status jobType } }`,
        { orderId: order.id },
      )
      setStatus('Resolution job enqueued. Polling for updates...')
      startPolling(order.id)
    } catch (e) {
      setStatus(`Error: ${(e as Error).message}`)
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

  return (
    <div>
      <section style={{ border: '1px solid #ddd', borderRadius: 8, padding: '1rem', marginBottom: '1rem' }}>
        <h2>1. Create order</h2>
        <p>
          <label>
            Owner ID <input value={ownerId} onChange={(e) => setOwnerId(e.target.value)} />
          </label>
        </p>
        <p>
          <label>
            Product type <input value={productType} onChange={(e) => setProductType(e.target.value)} />
          </label>
        </p>
        <p>
          <label>
            Width <input type="number" value={width} onChange={(e) => setWidth(Number(e.target.value))} />
          </label>{' '}
          <label>
            Height <input type="number" value={height} onChange={(e) => setHeight(Number(e.target.value))} />
          </label>{' '}
          <select value={unit} onChange={(e) => setUnit(e.target.value as 'in' | 'mm')}>
            <option value="in">in</option>
            <option value="mm">mm</option>
          </select>
        </p>
        <p>
          <label>
            Intent{' '}
            <select value={intent} onChange={(e) => setIntent(e.target.value as 'border' | 'full_bleed')}>
              <option value="full_bleed">full_bleed</option>
              <option value="border">border</option>
            </select>
          </label>
        </p>
        <p>
          <label>
            Customer request
            <br />
            <textarea value={customerRequest} onChange={(e) => setCustomerRequest(e.target.value)} rows={2} cols={50} />
          </label>
        </p>
        <button onClick={createOrder} disabled={!!order}>
          Create order
        </button>
      </section>

      {order && (
        <section style={{ border: '1px solid #ddd', borderRadius: 8, padding: '1rem', marginBottom: '1rem' }}>
          <h2>2. Upload artwork</h2>
          <input type="file" accept="image/png,image/jpeg" onChange={(e) => setFile(e.target.files?.[0] ?? null)} />
          <button onClick={uploadArtwork} disabled={!file}>
            Upload
          </button>
        </section>
      )}

      {order && order.intent === 'full_bleed' && (
        <section style={{ border: '1px solid #ddd', borderRadius: 8, padding: '1rem', marginBottom: '1rem' }}>
          <h2>3. Confirm trim</h2>
          <p>Full-bleed intent needs this before resolution/bleed can be measured instead of asked about.</p>
          <p>
            Current: <b>{order.artworkIsTrimOnly === null ? 'unconfirmed' : order.artworkIsTrimOnly ? 'trim-only (no bleed yet)' : 'not trim-only'}</b>
          </p>
          <button onClick={() => confirmTrim(true)}>This upload is trim-only (no bleed margin)</button>{' '}
          <button onClick={() => confirmTrim(false)}>This upload already has bleed included</button>
        </section>
      )}

      {order && (
        <section style={{ border: '1px solid #ddd', borderRadius: 8, padding: '1rem', marginBottom: '1rem' }}>
          <h2>4. Start resolution</h2>
          <button onClick={startResolution}>Start resolution</button>
        </section>
      )}

      {order &&
        order.artworkStatus === 'AWAITING_CLARIFICATION' &&
        (() => {
          const pending = order.clarifications.find((c) => c.answeredAt === null && c.invalidatedAt === null)
          if (!pending) return null
          return (
            <section style={{ border: '2px solid #d97706', borderRadius: 8, padding: '1rem', marginBottom: '1rem' }}>
              <h2>The agent has a question</h2>
              <p>{pending.question}</p>
              <button onClick={() => answerClarification(pending.id, 'yes')} style={{ marginRight: '0.5rem' }}>
                Yes
              </button>
              <button onClick={() => answerClarification(pending.id, 'no')}>No</button>
            </section>
          )
        })()}

      {status && (
        <p>
          <em>{status}</em>
        </p>
      )}

      {order && (
        <section style={{ border: '1px solid #ddd', borderRadius: 8, padding: '1rem' }}>
          <h2>Case status</h2>
          <table>
            <tbody>
              <tr>
                <td>Order ID</td>
                <td>{order.id}</td>
              </tr>
              <tr>
                <td>Case version</td>
                <td>{order.caseVersion}</td>
              </tr>
              <tr>
                <td>Artwork status</td>
                <td>
                  <b>{order.artworkStatus}</b>
                </td>
              </tr>
              <tr>
                <td>Proof status</td>
                <td>
                  <b>{order.proofStatus}</b>
                </td>
              </tr>
              <tr>
                <td>Production status</td>
                <td>
                  <b>{order.productionStatus}</b>
                </td>
              </tr>
            </tbody>
          </table>

          <h3>Jobs</h3>
          <ul>
            {order.jobs.length === 0 && <li>None yet.</li>}
            {order.jobs.map((j) => (
              <li key={j.id}>
                {j.jobType} — {j.status} (attempt {j.attemptCount}){j.lastError ? ` — ${j.lastError}` : ''}
              </li>
            ))}
          </ul>

          <h3>Findings</h3>
          <ul>
            {order.findings.length === 0 && <li>None yet.</li>}
            {order.findings.map((f) => (
              <li key={f.id}>
                {f.checkName}: <b>{f.result}</b> (rule {f.ruleVersion})
              </li>
            ))}
          </ul>

          <h3>Clarifications</h3>
          <ul>
            {order.clarifications.length === 0 && <li>None yet.</li>}
            {order.clarifications.map((c) => (
              <li key={c.id}>
                Q: {c.question}
                {c.answer ? (
                  <> — A: {c.answer}</>
                ) : c.invalidatedAt ? (
                  <> — (invalidated by a newer upload)</>
                ) : (
                  <> — (awaiting answer)</>
                )}
              </li>
            ))}
          </ul>
        </section>
      )}
    </div>
  )
}
