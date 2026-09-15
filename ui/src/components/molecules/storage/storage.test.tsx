import React from 'react'
import {act, render, cleanup, fireEvent, screen} from '@testing-library/react'
import Storage from './index'
import {defaultSettings, SIGNATURE, MessageTypes} from '../../../constants'
import {servedConnectionsEmitter, lifetimeConnectionsEmitter} from '../../../utils/wasmInterface'
jest.mock('../../../utils/wasmInterface', () => {
 const {StateEmitter} = jest.requireActual('../../../hooks/useStateEmitter')
 return {WasmInterface: class {}, servedConnectionsEmitter:new StateEmitter(0), lifetimeConnectionsEmitter:new StateEmitter(0), lifetimeChunksEmitter:new StateEmitter([]), sharingEmitter:new StateEmitter(false)}
})
beforeEach(()=> { process.env.REACT_APP_STORAGE_URL = 'https://storage.example.org/storage.html' })
afterEach(cleanup)
const connect = () => act(()=>servedConnectionsEmitter.update(servedConnectionsEmitter.state + 1))
const helped = (spy: jest.SpyInstance) => spy.mock.calls.filter(c=>c[0]?.data?.eventName==='helped')
test('two ready embeds report one live event exactly once',()=>{
 render(<><Storage settings={defaultSettings}/><Storage settings={defaultSettings}/></>)
 const frames=screen.getAllByTitle(`${SIGNATURE} iframe`) as HTMLIFrameElement[]
 const spies=frames.map(f=>jest.spyOn(f.contentWindow!, 'postMessage'))
 frames.forEach(f=>fireEvent.load(f))
 connect()
 expect(spies.flatMap(s=>helped(s))).toHaveLength(1)
})
test('buffers early activity until iframe load, excluding restored totals',()=>{
 render(<Storage settings={defaultSettings}/>)
 const frame=screen.getByTitle(`${SIGNATURE} iframe`) as HTMLIFrameElement
 const spy=jest.spyOn(frame.contentWindow!, 'postMessage')
 connect()
 expect(helped(spy)).toHaveLength(0)
 fireEvent.load(frame)
 expect(helped(spy)).toHaveLength(1)
 act(()=>window.dispatchEvent(new MessageEvent('message',{source:frame.contentWindow,data:{[SIGNATURE]:true,type:MessageTypes.STORAGE_GET,data:{lifetimeConnections:'10'}}})))
 expect(helped(spy)).toHaveLength(1)
 connect()
 expect(helped(spy)).toHaveLength(2)
})
test('reporting transfers to another loaded embed after unmount without replay',()=>{
 const {unmount}=render(<Storage settings={defaultSettings}/>)
 const frame=screen.getByTitle(`${SIGNATURE} iframe`) as HTMLIFrameElement
 const spy=jest.spyOn(frame.contentWindow!, 'postMessage')
 fireEvent.load(frame);connect();expect(helped(spy)).toHaveLength(1)
 unmount()
 render(<Storage settings={defaultSettings}/>)
 const other=screen.getByTitle(`${SIGNATURE} iframe`) as HTMLIFrameElement
 const otherSpy=jest.spyOn(other.contentWindow!, 'postMessage')
 fireEvent.load(other);expect(helped(otherSpy)).toHaveLength(0)
 connect();expect(helped(otherSpy)).toHaveLength(1)
})
test('restoration replies from other frames are ignored and own replies apply once',()=>{
 render(<Storage settings={defaultSettings}/>)
 const frame=screen.getByTitle(`${SIGNATURE} iframe`) as HTMLIFrameElement
 fireEvent.load(frame)
 const before=lifetimeConnectionsEmitter.state
 const data={[SIGNATURE]:true,type:MessageTypes.STORAGE_GET,data:{lifetimeConnections:'12'}}
 act(()=>window.dispatchEvent(new MessageEvent('message',{source:window,data})))
 expect(lifetimeConnectionsEmitter.state).toBe(before)
 for(let i=0;i<2;i++) act(()=>window.dispatchEvent(new MessageEvent('message',{source:frame.contentWindow,data})))
 expect(lifetimeConnectionsEmitter.state).toBe(before+12)
})
