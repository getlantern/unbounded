import {useContext, useEffect, useState} from 'react'
import {Layouts, Themes} from '../../../constants'
import {StyledEditor} from './styles'
import {AppContext} from '../../../context'
import {censoredCountryCodes} from '../../../utils/countries'

interface Props {
	embed: HTMLElement
}
const Editor = ({embed}: Props) => {
	const {settings} = useContext(AppContext)
	const [state, setState] = useState(settings)

	useEffect(() => {
		Object.keys(state).forEach(key => {
			// @ts-ignore
			embed.dataset[key] = state[key]
		})
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [state])

	return (
		<StyledEditor>
			<fieldset>
				<legend>Layout</legend>
				<div>
					<input
						type="radio"
						id={Layouts.BANNER}
						name="layout"
						value={Layouts.BANNER}
						checked={state.layout === Layouts.BANNER}
						onChange={() => setState({...state, layout: Layouts.BANNER})}
					/>
					<label htmlFor={Layouts.BANNER}>Banner</label>
				</div>

				<div>
					<input
						type="radio"
						id={Layouts.PANEL}
						name="layout"
						value={Layouts.PANEL}
						checked={state.layout === Layouts.PANEL}
						onChange={() => setState({...state, layout: Layouts.PANEL})}
					/>
					<label htmlFor={Layouts.PANEL}>Panel</label>
				</div>

				<div>
					<input
						type="radio"
						id={Layouts.FLOATING}
						name="layout"
						value={Layouts.FLOATING}
						checked={state.layout === Layouts.FLOATING}
						onChange={() => setState({...state, layout: Layouts.FLOATING})}
					/>
					<label htmlFor={Layouts.FLOATING}>Floating</label>
				</div>
			</fieldset>
			<fieldset>
				<legend>Theme</legend>
				<div>
					<input
						type="radio"
						id={Themes.DARK}
						name="theme"
						value={Themes.DARK}
						checked={state.theme === Themes.DARK}
						onChange={() => setState({...state, theme: Themes.DARK})}
					/>
					<label htmlFor={Themes.DARK}>Dark</label>
				</div>

				<div>
					<input
						type="radio"
						id={Themes.LIGHT}
						name="theme"
						value={Themes.LIGHT}
						checked={state.theme === Themes.LIGHT}
						onChange={() => setState({...state, theme: Themes.LIGHT})}
					/>
					<label htmlFor={Themes.LIGHT}>Light</label>
				</div>

				<div>
					<input
						type="radio"
						id={Themes.AUTO}
						name="theme"
						value={Themes.AUTO}
						checked={state.theme === Themes.AUTO}
						onChange={() => setState({...state, theme: Themes.AUTO})}
					/>
					<label htmlFor={Themes.AUTO}>Auto</label>
				</div>
			</fieldset>
			<fieldset>
				<legend>Features</legend>
				<div>
					<input
						type="checkbox"
						id="globe"
						name="globe"
						checked={state.globe}
						onChange={e => setState({...state, globe: e.target.checked})}
					/>
						<label htmlFor="globe">Globe</label>
				</div>

				<div>
					<input
						type="checkbox"
						id="exit"
						name="exit"
						checked={state.exit}
						onChange={e => setState({...state, exit: e.target.checked})}
					/>
					<label htmlFor="exit">Exit notification</label>
				</div>

				<div>
					<input
						type="checkbox"
						id="mobileBg"
						name="mobileBg"
						checked={state.mobileBg}
						onChange={e => setState({...state, mobileBg: e.target.checked})}
					/>
					<label htmlFor="mobileBg">Mobile background</label>
				</div>

				<div>
					<input
						type="checkbox"
						id="desktopBg"
						name="desktopBg"
						checked={state.desktopBg}
						onChange={e => setState({...state, desktopBg: e.target.checked})}
					/>
					<label htmlFor="desktopBg">Desktop background</label>
				</div>

				<div>
					<input
						type="checkbox"
						id="keepText"
						name="keepText"
						checked={state.keepText}
						onChange={e => setState({...state, keepText: e.target.checked})}
					/>
					<label htmlFor="keepText">Keep text</label>
				</div>

				<div>
					<input
						type="checkbox"
						id="menu"
						name="menu"
						checked={state.menu}
						onChange={e => setState({...state, menu: e.target.checked})}
					/>
					<label htmlFor="menu">Menu</label>
				</div>

				<div>
					<input
						type="checkbox"
						id="editor"
						name="editor"
						checked={state.editor}
						onChange={e => setState({...state, editor: e.target.checked})}
					/>
					<label htmlFor="editor">Editor</label>
				</div>

				<div>
					<input
						type="checkbox"
						id="collapse"
						name="collapse"
						checked={state.collapse}
						onChange={e => setState({...state, collapse: e.target.checked})}
					/>
					<label htmlFor="collapse">Collapse</label>
				</div>

				<div>
					<input
						type="checkbox"
						id="branding"
						name="branding"
						checked={state.branding}
						onChange={e => setState({...state, branding: e.target.checked})}
					/>
					<label htmlFor="branding">Branding</label>
				</div>

				<div>
					<input
						type="checkbox"
						id="title"
						name="title"
						checked={state.title}
						onChange={e => setState({...state, title: e.target.checked})}
					/>
					<label htmlFor="title">Title</label>
				</div>
			</fieldset>
			<fieldset>
				<legend>Support country</legend>
				<select
					id="supportCountry"
					name="supportCountry"
					value={state.supportCountry}
					onChange={e => setState({...state, supportCountry: e.target.value})}
				>
					<option value="">Anyone, wherever needed</option>
					{censoredCountryCodes.map(code => (
						<option key={code} value={code}>{code}</option>
					))}
				</select>
			</fieldset>
		</StyledEditor>
	)
}

export default Editor