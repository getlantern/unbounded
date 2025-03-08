import React, {Suspense, useEffect, useLayoutEffect, useRef, useState} from 'react'
import {settingsEmitter} from './index'
import Layout from './layout'
import Toast from './components/molecules/toast'
import Banner from './components/organisms/banner'
import Panel from './components/organisms/panel'
import usePageVisibility from './hooks/usePageVisibility'
import {useEmitterState} from './hooks/useStateEmitter'
import {sharingEmitter, WasmInterface} from './utils/wasmInterface'
import {isMobile} from './utils/isMobile'
import Editor from './components/organisms/editor'
import Floating from "./components/organisms/floating";
import Storage from './components/molecules/storage'
import useMessaging from './hooks/useMessaging'
import {Targets, Layouts} from './constants'
import {AppContextProvider} from './context'
import useAutoUpdate, {AUTO_START_STORAGE_FLAG} from './hooks/useAutoUpdate'
import './i18n'

interface Props {
  appId: number
  embed: HTMLElement
}

const App = ({appId, embed}: Props) => {
  const isVisible = usePageVisibility()
  const sharing = useEmitterState(sharingEmitter)
  const settings = useEmitterState(settingsEmitter)[appId]
  const {mock, target} = settings
  // setup app-wide listeners
  useMessaging(target)
  useAutoUpdate(target)
  const [mobileBg, desktopBg] = [settings.mobileBg, settings.desktopBg]
  const wasmInterface = useRef<WasmInterface>()
  const [width, setWidth] = useState(0)

  useLayoutEffect(() => {
    if (wasmInterface.current) return // already initialized or initializing
    const init = async (wasmInterface: WasmInterface) => {
      const instance = await wasmInterface.initialize({mock, target})
      if (!instance) return console.warn('wasm failed to initialize')
      // If the offscreen extension just auto updated and user was sharing pre-update, we will start after init
      // see useAutoUpdate hook for more details on this logic @todo move auto start to a config
      if (target === Targets.EXTENSION_OFFSCREEN) {
        if (localStorage.getItem(AUTO_START_STORAGE_FLAG)) {
          localStorage.removeItem(AUTO_START_STORAGE_FLAG)
          console.log('Auto starting due to auto update')
          wasmInterface.start()
        }
      }
    }
    wasmInterface.current = new WasmInterface()
    // on web, we don't need to initialize wasm until user starts sharing
    // see control/index.tsx for web init
    if (target === Targets.WEB) return
    init(wasmInterface.current).then(() => console.log(`p2p ${mock ? '"wasm"' : 'wasm'} initialized!`))
  }, [mock, target])

  useEffect(() => {
    // If settings for running in bg are disabled, we will stop the wasm when page is not visible
    // for more than a minute. This is to prevent it from running in the background unintentionally
    // when the user has navigated away from the page.
    // On mobile, it's safe to assume if a timeout is still running in the background, so is the wasm.
    // So we can rely on the timeout to stop wasm, otherwise the OS has already suspended the both the
    // timeout and wasm process.
    let timeout: ReturnType<typeof setTimeout> | null = null
    const createTimeout = () => {
      console.log(`creating timeout to stop wasm. isMobile: ${isMobile}, mobileBg: ${mobileBg}, desktopBg: ${desktopBg}`)
      timeout = setTimeout(() => {
        console.log(`stopping wasm due to timeout. isMobile: ${isMobile}, mobileBg: ${mobileBg}, desktopBg: ${desktopBg}, wasmInterface: ${!!wasmInterface.current}`)
        wasmInterface.current && wasmInterface.current.stop()
        console.log(`wasm stopped due to timeout. isMobile: ${isMobile}, mobileBg: ${mobileBg}, desktopBg: ${desktopBg}, wasmInterface: ${!!wasmInterface.current}`)
      }, 1000 * 60)
    }

    if (!isVisible && sharing) {
      console.log(`page not visible. isVisible: ${isVisible}, sharing: ${sharing}`)
      if (isMobile && !mobileBg) createTimeout()
      else if (!isMobile && !desktopBg) createTimeout()
    }
    return () => {
      if (timeout) {
        console.log(`clearing timeout to stop wasm. isMobile: ${isMobile}, mobileBg: ${mobileBg}, desktopBg: ${desktopBg}`)
        clearTimeout(timeout)
      }
    }
  }, [isVisible, sharing, mobileBg, desktopBg])

  return (
    <AppContextProvider value={{width, setWidth, settings, wasmInterface: wasmInterface.current!}}>
      { settings.target !== Targets.EXTENSION_POPUP && <Storage settings={settings} /> }
      { settings.editor && <Editor embed={embed} /> }
      <Layout>
        <Suspense fallback={'Loading..'}>
          <div id="geo-modal"></div>
          <Toast/>
          { settings.layout === Layouts.BANNER && (
            <Banner />
          )}
          { settings.layout === Layouts.PANEL && (
            <Panel />
          )}
          { settings.layout === Layouts.FLOATING && (
            <Floating />
          )}
        </Suspense>
      </Layout>
    </AppContextProvider>
  );
}

export default App
