import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'
import { bootstrapLogin } from './auth/login'
import './styles.css'

const rootElement = document.getElementById('root')
if (rootElement === null) {
  throw new Error('Drove root element is missing')
}
const root = ReactDOM.createRoot(rootElement)

void bootstrapLogin().then((result) => {
  switch (result.kind) {
    case 'ready':
      root.render(
        <React.StrictMode>
          <App />
        </React.StrictMode>,
      )
      return
    case 'reload':
      window.location.reload()
      return
    case 'failed':
      root.render(
        <main className="login-shell">
          <section className="login-panel" aria-labelledby="login-title">
            <p className="brand-mark">Drove</p>
            <h1 id="login-title">无法登录控制台</h1>
            <p>{result.message}</p>
          </section>
        </main>,
      )
      return
    default: {
      const unhandled: never = result
      return unhandled
    }
  }
})
